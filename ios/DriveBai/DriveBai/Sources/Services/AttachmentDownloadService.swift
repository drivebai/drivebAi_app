import Foundation
import os

/// Downloads remote attachment files to the on-disk caches directory so they
/// can be rendered by PDFKit / QuickLook / a share sheet without staying in
/// memory. The `/uploads/*` route on the backend is public, but the helper
/// still attaches the user's Bearer token when one is available so the same
/// code path also works for any future authenticated endpoint.
enum AttachmentDownloadError: LocalizedError {
    case invalidResponse
    case http(status: Int)
    case missingFile

    var errorDescription: String? {
        switch self {
        case .invalidResponse:    return "The server response was not a valid HTTP response."
        case .http(let status):   return "Download failed (HTTP \(status))."
        case .missingFile:        return "The downloaded file could not be read."
        }
    }
}

struct AttachmentDownloadService {
    static let shared = AttachmentDownloadService()

    private let keychain: KeychainService
    private let session: URLSession
    private let fileManager: FileManager
    private let logger = Logger(subsystem: "com.drivebai", category: "AttachmentDownload")

    init(
        keychain: KeychainService = .shared,
        session: URLSession = .shared,
        fileManager: FileManager = .default
    ) {
        self.keychain = keychain
        self.session = session
        self.fileManager = fileManager
    }

    /// Downloads `url` and writes it to the caches directory using the given
    /// suggested filename (sanitized). Returns the local file URL.
    /// If the file already exists in the caches dir (re-opening the same
    /// immutable UUID-named upload), the existing copy is returned without a
    /// network round-trip.
    func download(url: URL, suggestedFilename: String) async throws -> URL {
        let destination = cachesDestination(for: suggestedFilename, fallbackURL: url)
        if fileManager.fileExists(atPath: destination.path) {
            logger.debug("Cache hit for \(url.absoluteString)")
            return destination
        }

        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        if let token = keychain.getAccessToken() {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        request.setValue("application/octet-stream, application/pdf, */*", forHTTPHeaderField: "Accept")

        logger.info("Downloading attachment \(url.absoluteString)")
        let (tempURL, response) = try await session.download(for: request)

        guard let http = response as? HTTPURLResponse else {
            throw AttachmentDownloadError.invalidResponse
        }
        guard (200...299).contains(http.statusCode) else {
            logger.error("Attachment HTTP \(http.statusCode) for \(url.absoluteString)")
            throw AttachmentDownloadError.http(status: http.statusCode)
        }

        try? fileManager.removeItem(at: destination)
        do {
            try fileManager.moveItem(at: tempURL, to: destination)
        } catch {
            logger.error("Move into caches failed: \(error.localizedDescription)")
            throw AttachmentDownloadError.missingFile
        }
        logger.info("Saved attachment to \(destination.path)")
        return destination
    }

    // MARK: Private

    private func cachesDestination(for suggested: String, fallbackURL: URL) -> URL {
        let baseDir = fileManager.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("ChatAttachments", isDirectory: true)
        if !fileManager.fileExists(atPath: baseDir.path) {
            try? fileManager.createDirectory(at: baseDir, withIntermediateDirectories: true)
        }
        let safe = sanitize(filename: suggested.isEmpty ? fallbackURL.lastPathComponent : suggested)
        return baseDir.appendingPathComponent(safe)
    }

    private func sanitize(filename: String) -> String {
        // Strip path separators and control characters; keep a stable filename
        // so re-downloading the same attachment overwrites the same on-disk
        // file rather than littering the caches dir with copies.
        let cleaned = filename
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "\\", with: "_")
            .replacingOccurrences(of: "\0", with: "")
        return cleaned.isEmpty ? "attachment" : cleaned
    }
}
