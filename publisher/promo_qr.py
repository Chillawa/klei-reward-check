import json
import os
import shutil
import subprocess
import sys
from functools import lru_cache
from pathlib import Path

try:
    import cv2
    import numpy as np
    from scipy.ndimage import gaussian_filter
    from scipy.optimize import minimize
except ImportError:
    cv2 = None


class UnreadableQR(ValueError):
    def __init__(self, decoded=()):
        super().__init__('QR pattern detected but its payload could not be decoded')
        self.decoded = list(decoded)


@lru_cache(maxsize=40)
def _function_pattern(size):
    """White/black modules specified by QR Model 2, independent of the payload."""
    pattern = np.full((size, size), np.nan)
    for x, y in ((0, 0), (size - 7, 0), (0, size - 7)):
        for i in range(-1, 8):
            for j in range(-1, 8):
                if 0 <= x + i < size and 0 <= y + j < size:
                    radius = max(abs(i - 3), abs(j - 3))
                    pattern[y + j, x + i] = float(radius == 2 or radius >= 4)
    axis = np.arange(7, size - 7)
    pattern[6, axis] = pattern[axis, 6] = axis % 2
    if size > 21:
        y, x = np.indices((5, 5))
        pattern[-9:-4, -9:-4] = np.maximum(abs(x - 2), abs(y - 2)) == 1
    return pattern


@lru_cache(maxsize=40)
def _sample_positions(size):
    pattern = _function_pattern(size)
    y, x = np.where(np.isfinite(pattern))
    return x, y, pattern[y, x] == 1


def _square(side):
    return np.float32([[0, 0], [side, 0], [side, side], [0, side]])


def _panels(gray):
    # Rectify light labels on packaging even when perspective hides the finder squares.
    edges = cv2.adaptiveThreshold(gray, 255, cv2.ADAPTIVE_THRESH_GAUSSIAN_C,
                                  cv2.THRESH_BINARY, 61, -4)
    contours, _ = cv2.findContours(edges, cv2.RETR_LIST, cv2.CHAIN_APPROX_SIMPLE)
    for contour in contours:
        if cv2.contourArea(contour) < 1200:
            continue
        quad = cv2.approxPolyDP(contour, cv2.arcLength(contour, True) * .025, True)
        if len(quad) != 4 or not cv2.isContourConvex(quad):
            continue
        corners = np.float32(quad[:, 0])
        if min(np.linalg.norm(corners - np.roll(corners, 1, axis=0), axis=1)) < 20:
            continue
        if cv2.contourArea(corners, oriented=True) < 0:
            corners = corners[::-1]
        yield np.roll(corners, -corners.sum(axis=1).argmin(), axis=0)


def _printed_grid(gray, panel):
    side = 512
    transform = cv2.getPerspectiveTransform(panel, _square(side - 1))
    rectified = cv2.warpPerspective(gray, transform, (side, side))
    views = [np.rot90(rectified, turn) for turn in range(4)]
    best = None
    # Small QR codes printed with a quiet zone are particularly vulnerable to blur.
    for size in range(21, 70, 4):
        x, y, white = _sample_positions(size)
        for margin in (4, 3, 5):
            scale = side / (size + 2 * margin)
            xs = np.int32((x + margin + .5) * scale)
            ys = np.int32((y + margin + .5) * scale)
            for turn, view in enumerate(views):
                values = view[ys, xs].astype('float32')
                contrast = values[white].mean() - values[~white].mean()
                if contrast < 30:
                    continue
                correlation = contrast / max(20., float(values.std()))
                if correlation < 1.12 or (best and correlation <= best[0]):
                    continue
                best = (correlation, turn, size, margin)
    if best:
        _, turn, size, margin = best
        panel = np.roll(panel, -turn, axis=0)
        transform = cv2.getPerspectiveTransform(_square(size + 2 * margin), panel)
        corners = cv2.perspectiveTransform((_square(size) + margin)[None], transform)[0]
        return size, corners


def _refine_corners(image, corners, size):
    # Refine the homography against known finder, timing and alignment modules.
    square = _square(size * 8)
    transform = cv2.getPerspectiveTransform(corners, square)
    patch = cv2.warpPerspective(image, transform, (size * 8, size * 8), flags=cv2.INTER_CUBIC)
    x, y, white = _sample_positions(size)
    positions = np.float32(np.stack((x + .5, y + .5), axis=-1)[:, None])
    weights = np.float32(white / white.sum() - (~white) / (~white).sum())

    def cost(offsets):
        shifted = square + np.float32(offsets.reshape(4, 2))
        projection = cv2.getPerspectiveTransform(square / 8, shifted)
        coords = cv2.perspectiveTransform(positions, projection)
        values = cv2.remap(patch, coords, None, cv2.INTER_LINEAR).mean(axis=(-1, -2))
        return -float(values @ weights)

    fit = minimize(cost, np.zeros(8), method='Powell', bounds=[(-6, 6)] * 8,
                   options={'xtol': .15, 'maxiter': 7})
    shifted = square + np.float32(fit.x.reshape(4, 2))
    return cv2.perspectiveTransform(shifted[None], cv2.getPerspectiveTransform(square, corners))[0]


def _read_printed_grid(image, corners, size):
    # A local linear filter trained on fixed modules compensates for directional print blur.
    step, margin = 8, 16
    side = size * step
    transform = cv2.getPerspectiveTransform(corners, _square(side) + margin)
    patch = cv2.warpPerspective(image, transform, (side + 2 * margin,) * 2, flags=cv2.INTER_CUBIC)
    center = margin + step // 2
    samples = [patch[center + y:center + y + side:step, center + x:center + x + side:step]
               for x in range(-step, step + 1, 2) for y in range(-step, step + 1, 2)]
    raw = np.concatenate(samples, axis=-1).astype('float32') / 255
    features = raw - gaussian_filter(raw, (2, 2, 0))
    pattern = _function_pattern(size)
    known = np.isfinite(pattern)
    predictors = features[known]
    gram = predictors.T @ predictors
    target = predictors.T @ (pattern[known] - .5)
    detector = cv2.QRCodeDetector()
    bounds = (_square(size * 5 - 1) + 20)[None]
    for penalty in (1., .1, .01):
        weights = np.linalg.solve(gram + np.eye(gram.shape[0]) * penalty, target)
        predicted = features @ weights + .5
        for threshold in (.5, .45, .55):
            bits = np.uint8((predicted > threshold) * 255)
            bits[known] = pattern[known] * 255
            symbol = np.pad(bits, 4, constant_values=255).repeat(5, 0).repeat(5, 1)
            value, _ = detector.decode(symbol, bounds)
            if value:
                return value
    return ''


def _overlaps(corners, others):
    area = cv2.contourArea(corners)
    return any(cv2.intersectConvexConvex(corners, other)[0] > .65 * min(area, cv2.contourArea(other))
               for other in others)


def _decode_image(data):
    if not data:
        raise ValueError('Image file is empty')
    image = cv2.imdecode(np.frombuffer(data, np.uint8), cv2.IMREAD_COLOR)
    if image is None:
        raise ValueError('Image format could not be decoded')
    gray = cv2.cvtColor(image, cv2.COLOR_BGR2GRAY)
    decoded, resolved, detected = [], [], []

    def record(value, corners):
        if value and not _overlaps(corners, resolved):
            resolved.append(corners)
            decoded.append(value)
        if not value:
            detected.append(corners)

    detectors = [cv2.QRCodeDetector()]
    if hasattr(cv2, 'QRCodeDetectorAruco'):
        detectors.append(cv2.QRCodeDetectorAruco())
    for detector in detectors:
        _, values, points, _ = detector.detectAndDecodeMulti(gray)
        if points is not None:
            for i, corners in enumerate(points):
                record(values[i] if i < len(values) else '', corners)
        else:
            value, points, _ = detector.detectAndDecode(gray)
            if points is not None:
                record(value, points[0])

    for panel in _panels(gray):
        if _overlaps(panel, resolved):
            continue
        grid = _printed_grid(gray, panel)
        if grid:
            size, corners = grid
            refined = _refine_corners(image, corners, size)
            record(_read_printed_grid(image, refined, size), corners)

    if any(not _overlaps(corners, resolved) for corners in detected):
        raise UnreadableQR(decoded)
    return decoded


def decode_public_qr(data: bytes) -> list[str]:
    """Return decoded text per symbol or report any identified, unreadable QR."""
    if cv2 is not None:
        try:
            return _decode_image(data)
        except cv2.error as exc:
            raise ValueError('Image processing failed') from exc
    interpreters = [shutil.which('python3'), '/Library/Frameworks/Python.framework/Versions/Current/bin/python3',
                    '/opt/homebrew/bin/python3', '/usr/local/bin/python3']
    tried = {os.path.realpath(sys.executable)}
    for executable in filter(None, interpreters):
        path = os.path.realpath(executable)
        if path in tried or not os.access(path, os.X_OK):
            continue
        tried.add(path)
        try:
            result = subprocess.run([path, str(Path(__file__).resolve())], input=data,
                                    capture_output=True, timeout=45,
                                    creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise ValueError('Local image decoder could not complete') from exc
        if result.returncode == 2:
            continue
        if result.returncode == 3:
            raise UnreadableQR(json.loads(result.stdout))
        if result.returncode == 0:
            return json.loads(result.stdout)
        raise ValueError('Image processing failed in the local decoder')
    raise ValueError('Public QR decoding requires a local Python with OpenCV, NumPy and SciPy')


if __name__ == '__main__':
    if cv2 is None:
        sys.exit(2)
    try:
        print(json.dumps(decode_public_qr(sys.stdin.buffer.read())))
    except UnreadableQR as exc:
        print(json.dumps(exc.decoded))
        sys.exit(3)
    except ValueError:
        sys.exit(1)
