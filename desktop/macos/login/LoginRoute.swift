import Foundation
import Network
import WebKit

private let kleiAccountHost = "accounts.klei.com"

// The health probe must not read the user's session or follow them to a provider.
private final class LoginProbeDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

private func publicRequest(_ url: URL, method: String, timeout: TimeInterval,
                           completion: @escaping (Data?, HTTPURLResponse?) -> Void) {
    let settings = URLSessionConfiguration.ephemeral
    settings.httpShouldSetCookies = false
    settings.httpCookieStorage = nil
    settings.urlCredentialStorage = nil
    settings.urlCache = nil
    settings.timeoutIntervalForResource = timeout
    let session = URLSession(configuration: settings, delegate: LoginProbeDelegate(), delegateQueue: nil)
    var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: timeout)
    request.httpMethod = method
    session.dataTask(with: request) { data, response, _ in
        session.finishTasksAndInvalidate()
        completion(data, response as? HTTPURLResponse)
    }.resume()
}

private struct DNSReply: Decodable {
    struct Answer: Decodable {
        let type: Int
        let data: String
    }
    let Status: Int
    let Answer: [Answer]?
}

private func publicIPv4(_ text: String) -> IPv4Address? {
    guard let address = IPv4Address(text) else { return nil }
    let parts = Array(address.rawValue)
    let a = parts[0], b = parts[1]
    guard a != 0, a != 10, a != 127, a < 224,
          !(a == 100 && (64...127).contains(b)), !(a == 169 && b == 254),
          !(a == 172 && (16...31).contains(b)), !(a == 192 && b == 168),
          !(a == 198 && (18...19).contains(b)) else { return nil }
    return address
}

// Only the official account host's A records are looked up. WebKit owns the TLS
// session to Klei, including hostname and certificate checks, inside the tunnel.
final class KleiLoginRoute {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "klei.login.connections")
    private let addresses: [IPv4Address]
    private var tunnels: [UUID: KleiTunnel] = [:]

    private init?(addresses: [IPv4Address]) {
        self.addresses = addresses
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: .ipv4(.loopback), port: .any)
        guard let listener = try? NWListener(using: parameters) else { return nil }
        self.listener = listener
    }

    static func prepare(for store: WKWebsiteDataStore, completion: @escaping (KleiLoginRoute?) -> Void) {
        guard #available(macOS 14, *) else { completion(nil); return }
        let login = URL(string: "https://accounts.klei.com/login")!
        publicRequest(login, method: "HEAD", timeout: 4) { _, response in
            if let response, (200..<400).contains(response.statusCode) {
                DispatchQueue.main.async { completion(nil) }
                return
            }
            let dns = URL(string: "https://dns.google/resolve?name=accounts.klei.com&type=A")!
            publicRequest(dns, method: "GET", timeout: 6) { data, response in
                guard response?.statusCode == 200, let data, data.count < 65536,
                      let reply = try? JSONDecoder().decode(DNSReply.self, from: data), reply.Status == 0,
                      let answers = reply.Answer else {
                    DispatchQueue.main.async { completion(nil) }
                    return
                }
                let addresses = answers.filter { $0.type == 1 }.compactMap { publicIPv4($0.data) }
                guard !addresses.isEmpty, let route = KleiLoginRoute(addresses: addresses) else {
                    DispatchQueue.main.async { completion(nil) }
                    return
                }
                route.start { port in
                    DispatchQueue.main.async {
                        guard let port else { completion(nil); return }
                        var proxy = ProxyConfiguration(httpCONNECTProxy: .hostPort(host: .ipv4(.loopback), port: port))
                        proxy.matchDomains = [kleiAccountHost]
                        store.proxyConfigurations = [proxy]
                        completion(route)
                    }
                }
            }
        }
    }

    private func start(_ started: @escaping (NWEndpoint.Port?) -> Void) {
        listener.stateUpdateHandler = { [weak self] state in
            guard let self else { return }
            switch state {
            case .ready:
                self.listener.stateUpdateHandler = nil
                started(self.listener.port)
            case .failed, .cancelled:
                self.listener.stateUpdateHandler = nil
                self.listener.cancel()
                started(nil)
            default: break
            }
        }
        listener.newConnectionHandler = { [weak self] client in
            guard let self, self.tunnels.count < 32 else { client.cancel(); return }
            let id = UUID()
            let tunnel = KleiTunnel(client: client, addresses: self.addresses, queue: self.queue) { [weak self] in
                self?.tunnels.removeValue(forKey: id)
            }
            self.tunnels[id] = tunnel
            tunnel.start()
        }
        listener.start(queue: queue)
    }

    deinit { listener.cancel() }
}

private final class KleiTunnel {
    private let client: NWConnection
    private let addresses: [IPv4Address]
    private let queue: DispatchQueue
    private let finished: () -> Void
    private var upstream: NWConnection?
    private var header = Data()
    private var forwarding = false
    private var closed = false

    init(client: NWConnection, addresses: [IPv4Address], queue: DispatchQueue, finished: @escaping () -> Void) {
        self.client = client
        self.addresses = addresses
        self.queue = queue
        self.finished = finished
    }

    func start() {
        client.stateUpdateHandler = { [weak self] state in
            if case .failed = state { self?.close() }
        }
        client.start(queue: queue)
        queue.asyncAfter(deadline: .now() + 12) { [weak self] in
            if self?.forwarding == false { self?.close() }
        }
        readConnect()
    }

    private func readConnect() {
        client.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] data, _, eof, error in
            guard let self, !self.closed else { return }
            guard let data, error == nil, !eof else { self.close(); return }
            self.header.append(data)
            let delimiter = Data("\r\n\r\n".utf8)
            guard let split = self.header.range(of: delimiter) else {
                if self.header.count >= 8192 { self.close() } else { self.readConnect() }
                return
            }
            guard split.lowerBound < 8192,
                  let request = String(data: self.header[..<split.lowerBound], encoding: .ascii),
                  let line = request.components(separatedBy: "\r\n").first else { self.close(); return }
            let fields = line.split(separator: " ")
            guard fields.count == 3, fields[0] == "CONNECT", fields[1].lowercased() == "\(kleiAccountHost):443",
                  fields[2] == "HTTP/1.1" || fields[2] == "HTTP/1.0" else {
                self.client.send(content: Data("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n".utf8),
                                 completion: .contentProcessed { [weak self] _ in self?.close() })
                return
            }
            self.header.removeSubrange(..<split.upperBound)
            self.connect(at: 0)
        }
    }

    private func connect(at index: Int) {
        guard index < addresses.count else { close(); return }
        let target = NWConnection(host: .ipv4(addresses[index]), port: 443, using: .tcp)
        upstream = target
        let tryNext = { [weak self, weak target] in
            guard let self, let target, self.upstream === target, !self.forwarding, !self.closed else { return }
            target.stateUpdateHandler = nil
            target.cancel()
            self.connect(at: index + 1)
        }
        target.stateUpdateHandler = { [weak self, weak target] state in
            guard let self, let target, self.upstream === target, !self.closed else { return }
            switch state {
            case .ready:
                self.forwarding = true
                self.client.send(content: Data("HTTP/1.1 200 Connection Established\r\n\r\n".utf8),
                                 completion: .contentProcessed { [weak self] error in
                    guard let self else { return }
                    if error != nil { self.close(); return }
                    self.forward(from: target, to: self.client)
                    target.send(content: self.header.isEmpty ? nil : self.header,
                                completion: .contentProcessed { [weak self] error in
                        guard let self else { return }
                        if error != nil { self.close() } else { self.forward(from: self.client, to: target) }
                    })
                })
            case .failed:
                if self.forwarding { self.close() } else { tryNext() }
            default: break
            }
        }
        target.start(queue: queue)
        queue.asyncAfter(deadline: .now() + 3, execute: tryNext)
    }

    private func forward(from source: NWConnection, to destination: NWConnection) {
        guard !closed else { return }
        source.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] bytes, _, eof, error in
            guard let self, !self.closed else { return }
            guard let bytes, !bytes.isEmpty, error == nil else { self.close(); return }
            destination.send(content: bytes, completion: .contentProcessed { [weak self] error in
                guard let self else { return }
                if error != nil || eof { self.close() } else { self.forward(from: source, to: destination) }
            })
        }
    }

    private func close() {
        guard !closed else { return }
        closed = true
        client.cancel()
        upstream?.cancel()
        finished()
    }

    deinit {
        client.cancel()
        upstream?.cancel()
    }
}
