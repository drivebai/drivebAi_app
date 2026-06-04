import Foundation
import UIKit
import ImageIO
import CryptoKit
import os

/// Shared image pipeline used by every remote-image view in the app.
///
/// Layered cache, fastest first:
///   1. NSCache in memory, keyed by `url|maxPixelSize` — decoded UIImage, ready
///      to render.
///   2. On-disk thumbnail at `Caches/ImagePipeline/<sha256(key)>` — raw bytes
///      of the downsampled JPEG/HEIC ready for ImageIO.
///   3. URLSession's URLCache (50 MB mem / 100 MB disk) — full-resolution HTTP
///      response. Honors Cache-Control / ETag automatically.
///   4. Network.
///
/// All decoding + downsampling runs on a detached `userInitiated` task so the
/// main thread never blocks on a JPEG decode. Concurrent requests for the same
/// `url|size` key share one in-flight task (request coalescing) so a chat with
/// 20 thumbnails of the same image triggers exactly one network round-trip.
///
/// Backed by `os_signpost` so loading and decoding regions are visible in
/// Instruments → Points of Interest.
enum ImagePipeline {

    /// Shared singleton actor — call sites use the synchronous enum facade.
    static let shared = ImagePipelineActor()

    /// Load (or fetch) an image, downsampled to fit `maxPixelSize` pixels on
    /// its longest edge. The result is a fully-decoded `UIImage` whose CGImage
    /// is materialized so SwiftUI can render it on the next frame without a
    /// main-thread decode hit.
    static func load(url: URL, maxPixelSize: CGFloat) async throws -> UIImage {
        try await shared.loadImage(url: url, maxPixelSize: maxPixelSize)
    }

    /// Cancel an in-flight request. Safe to call when no request is pending.
    static func cancel(url: URL, maxPixelSize: CGFloat) async {
        await shared.cancel(url: url, maxPixelSize: maxPixelSize)
    }

    /// Best-effort prefetch — useful in `onAppear` for the next N rows of a
    /// scrolling list. Errors are silently swallowed.
    static func prefetch(urls: [URL], maxPixelSize: CGFloat) {
        Task.detached(priority: .utility) {
            for url in urls {
                _ = try? await shared.loadImage(url: url, maxPixelSize: maxPixelSize)
            }
        }
    }
}

actor ImagePipelineActor {

    // MARK: Storage

    private let memCache: NSCache<NSString, UIImage>
    private let session: URLSession
    private let cacheDir: URL
    private var inFlight: [String: Task<UIImage, Error>] = [:]

    private let logger = Logger(subsystem: "com.drivebai", category: "ImagePipeline")
    private let signposter = OSSignposter(subsystem: "com.drivebai", category: "ImagePipeline")

    init() {
        // URLCache handles HTTP-level caching (Cache-Control, ETag, 304).
        // 100 MB on disk is roughly 200 photos at the average chat-attachment
        // size; oversized URLCache buys nothing because we also keep the
        // downsampled bytes on our own disk cache below.
        let urlCache = URLCache(
            memoryCapacity: 50 * 1024 * 1024,
            diskCapacity: 100 * 1024 * 1024
        )
        let cfg = URLSessionConfiguration.default
        cfg.urlCache = urlCache
        cfg.requestCachePolicy = .useProtocolCachePolicy
        cfg.httpMaximumConnectionsPerHost = 8
        cfg.timeoutIntervalForRequest = 30
        self.session = URLSession(configuration: cfg)

        let mem = NSCache<NSString, UIImage>()
        mem.totalCostLimit = 100 * 1024 * 1024 // 100 MB of decoded pixel buffers
        mem.countLimit = 256
        self.memCache = mem

        // Disk cache lives in Caches so iOS can reclaim space under pressure.
        let base = (try? FileManager.default.url(
            for: .cachesDirectory, in: .userDomainMask,
            appropriateFor: nil, create: true
        )) ?? FileManager.default.temporaryDirectory
        let dir = base.appendingPathComponent("ImagePipeline", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        self.cacheDir = dir
    }

    // MARK: Public

    func loadImage(url: URL, maxPixelSize: CGFloat) async throws -> UIImage {
        let key = cacheKey(url: url, size: maxPixelSize)
        let nsKey = key as NSString

        // 1. Memory hit — ready to render this frame.
        if let cached = memCache.object(forKey: nsKey) {
            logger.debug("MEM hit: \(url.absoluteString) @\(Int(maxPixelSize))px")
            return cached
        }

        // 2. Coalesce duplicate concurrent loads onto one Task.
        if let existing = inFlight[key] {
            logger.debug("COALESCE: \(url.absoluteString) @\(Int(maxPixelSize))px")
            return try await existing.value
        }

        // 3. Start a single shared task.
        let diskURL = diskPath(for: key)
        let session = self.session
        let signposter = self.signposter
        let logger = self.logger

        let task = Task<UIImage, Error> { [weak self] in
            // Disk hit — decode in background.
            if FileManager.default.fileExists(atPath: diskURL.path),
               let data = try? Data(contentsOf: diskURL),
               let img = await Self.decode(data: data, maxPixelSize: maxPixelSize, signposter: signposter)
            {
                logger.debug("DISK hit: \(url.absoluteString) @\(Int(maxPixelSize))px")
                await self?.store(image: img, forKey: key)
                return img
            }

            // Network.
            let downloadState = signposter.beginInterval("ImageDownload", id: signposter.makeSignpostID(),
                                                         "\(url.absoluteString, privacy: .public)")
            let (data, response): (Data, URLResponse)
            do {
                (data, response) = try await session.data(from: url)
                signposter.endInterval("ImageDownload", downloadState)
            } catch {
                signposter.endInterval("ImageDownload", downloadState)
                logger.error("NET error: \(url.absoluteString) – \(error.localizedDescription)")
                throw error
            }
            if let http = response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
                logger.error("NET HTTP \(http.statusCode): \(url.absoluteString)")
                throw URLError(.badServerResponse)
            }
            logger.info("NET ok: \(url.absoluteString) (\(data.count)B)")

            guard let img = await Self.decode(data: data, maxPixelSize: maxPixelSize, signposter: signposter) else {
                throw URLError(.cannotDecodeRawData)
            }
            // Persist the downsampled bytes (re-encoded as JPEG to stabilize
            // size — we keep the original full-res only in URLCache).
            if let encoded = img.jpegData(compressionQuality: 0.9) {
                try? encoded.write(to: diskURL, options: .atomic)
            }
            await self?.store(image: img, forKey: key)
            return img
        }
        inFlight[key] = task
        defer { inFlight.removeValue(forKey: key) }

        do {
            let img = try await task.value
            return img
        } catch {
            // Don't poison the coalesce table — failure path is the same
            // because of the `defer` above.
            throw error
        }
    }

    func cancel(url: URL, maxPixelSize: CGFloat) {
        let key = cacheKey(url: url, size: maxPixelSize)
        inFlight[key]?.cancel()
        inFlight.removeValue(forKey: key)
    }

    // MARK: Internals

    private func store(image: UIImage, forKey key: String) {
        let cost = Int(image.size.width * image.size.height * image.scale * image.scale * 4)
        memCache.setObject(image, forKey: key as NSString, cost: cost)
    }

    private func cacheKey(url: URL, size: CGFloat) -> String {
        "\(url.absoluteString)|\(Int(size.rounded()))"
    }

    private func diskPath(for key: String) -> URL {
        let hash = SHA256.hash(data: Data(key.utf8))
        let hex = hash.map { String(format: "%02x", $0) }.joined()
        return cacheDir.appendingPathComponent(hex)
    }

    // MARK: Decode (off-main)

    /// Decode + downsample via ImageIO so we never instantiate the full-res
    /// bitmap in memory. Runs on a detached user-initiated Task so the main
    /// thread never blocks on JPEG parsing.
    private static func decode(data: Data, maxPixelSize: CGFloat, signposter: OSSignposter) async -> UIImage? {
        await Task.detached(priority: .userInitiated) {
            let id = signposter.makeSignpostID()
            let state = signposter.beginInterval("ImageDecode", id: id)
            defer { signposter.endInterval("ImageDecode", state) }

            guard let src = CGImageSourceCreateWithData(data as CFData, [
                kCGImageSourceShouldCache: false
            ] as CFDictionary) else { return nil }

            let opts: [CFString: Any] = [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceShouldCacheImmediately: true,
                kCGImageSourceThumbnailMaxPixelSize: max(maxPixelSize, 64)
            ]
            guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else {
                return nil
            }
            return UIImage(cgImage: cg, scale: UIScreen.main.scale, orientation: .up)
        }.value
    }
}
