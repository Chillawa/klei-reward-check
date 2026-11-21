import AppKit
import Foundation

enum PanelLanguage {
    static func identifier(for preferences: [String]) -> String {
        for preference in preferences {
            let parts = preference.lowercased().replacingOccurrences(of: "_", with: "-").split(separator: "-")
            if parts.first == "en" { return "en" }
            guard parts.first == "zh" else { continue }
            if parts.contains("hant") { return "zh-Hant" }
            if parts.contains("hans") { return "zh-Hans" }
            return parts.contains(where: { ["tw", "hk", "mo"].contains($0) }) ? "zh-Hant" : "zh-Hans"
        }
        return "en"
    }

    static let current = identifier(for: Locale.preferredLanguages)
    static let locale = Locale(identifier: current)
    static let strings: [String: [String: String]] = {
        let executable = Bundle.main.executableURL
        let resources = executable?.deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Resources/translations.json")
        for url in [Bundle.main.url(forResource: "translations", withExtension: "json"), resources].compactMap({ $0 }) {
            if let data = try? Data(contentsOf: url),
               let result = try? JSONDecoder().decode([String: [String: String]].self, from: data) { return result }
        }
        return [:]
    }()

    // Backend notices retain the original sentence and expand their arguments
    // before sending it. Match those complete sentences against the same catalog.
    private static let templates: [(String, NSRegularExpression)] = strings.keys.sorted { $0.count > $1.count }.compactMap { key in
        guard key.contains("{0}") else { return nil }
        var pattern = NSRegularExpression.escapedPattern(for: key)
        for index in 0..<10 {
            pattern = pattern.replacingOccurrences(of: NSRegularExpression.escapedPattern(for: "{\(index)}"), with: "(.+?)")
        }
        guard let regex = try? NSRegularExpression(pattern: "^" + pattern + "$") else { return nil }
        return (key, regex)
    }

    static func text(_ key: String, _ arguments: String...) -> String {
        format(key, arguments)
    }

    private static func format(_ key: String, _ arguments: [String]) -> String {
        var value = current == "zh-Hans" ? key : strings[key]?[current] ?? strings[key]?["en"] ?? key
        for (index, argument) in arguments.enumerated() {
            value = value.replacingOccurrences(of: "{\(index)}", with: argument)
        }
        return value
    }

    static func message(_ text: String) -> String {
        guard current != "zh-Hans" else { return text }
        return text.components(separatedBy: "\n").map { line in
            if strings[line] != nil { return format(line, []) }
            let sentences = line.components(separatedBy: "。")
            if sentences.count > 2 || (sentences.count == 2 && !sentences[1].isEmpty) {
                let parts = sentences.enumerated().compactMap { index, part -> String? in
                    guard !part.isEmpty else { return nil }
                    return message(part + (index < sentences.count - 1 ? "。" : ""))
                }
                return parts.joined(separator: current == "en" ? " " : "")
            }
            let range = NSRange(line.startIndex..., in: line)
            for (key, regex) in templates {
                guard let match = regex.firstMatch(in: line, range: range) else { continue }
                let arguments = (1..<match.numberOfRanges).compactMap {
                    Range(match.range(at: $0), in: line).map { String(line[$0]) }
                }
                return format(key, arguments)
            }
            return line
        }.joined(separator: "\n")
    }
}

enum PanelAppearance {
    private static let defaults: UserDefaults = {
        let domain = "local.klei-reward-check.desktop"
        return Bundle.main.bundleIdentifier == domain ? .standard : UserDefaults(suiteName: domain)!
    }()
    private static let key = "panelAppearance"

    static func restore() {
        switch defaults.string(forKey: key) {
        case "dark": NSApp.appearance = NSAppearance(named: .darkAqua)
        case "light": NSApp.appearance = NSAppearance(named: .aqua)
        default: NSApp.appearance = nil
        }
    }

    static func toggle(from appearance: NSAppearance) {
        if NSEvent.modifierFlags.contains(.option) {
            defaults.removeObject(forKey: key)
        } else {
            defaults.set(appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? "light" : "dark", forKey: key)
        }
        restore()
    }
}
