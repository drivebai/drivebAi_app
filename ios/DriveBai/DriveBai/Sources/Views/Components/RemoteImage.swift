import SwiftUI
import os

/// A URLSession-backed image view that logs HTTP status/errors
/// unlike AsyncImage which swallows all error details.
struct RemoteImage: View {
    let url: URL
    var contentMode: ContentMode = .fill

    @State private var phase: ImagePhase = .loading

    private static let logger = Logger(subsystem: "com.drivebai", category: "RemoteImage")

    enum ImagePhase {
        case loading
        case success(Image)
        case failure(String)
    }

    var body: some View {
        ZStack {
            switch phase {
            case .loading:
                Rectangle()
                    .fill(Color.driveBaiPrimary.opacity(0.1))
                    .overlay(ProgressView())
            case .success(let image):
                Color.clear
                    .overlay(
                        image
                            .resizable()
                            .aspectRatio(contentMode: contentMode)
                    )
                    .clipped()
            case .failure(let reason):
                Rectangle()
                    .fill(Color.driveBaiPrimary.opacity(0.15))
                    .overlay(
                        VStack(spacing: 4) {
                            Image(systemName: "exclamationmark.triangle")
                                .font(.system(size: 24))
                                .foregroundColor(Color.driveBaiPrimary.opacity(0.5))
                            #if DEBUG
                            Text(reason)
                                .font(.system(size: 9))
                                .foregroundColor(.secondary)
                                .lineLimit(2)
                                .padding(.horizontal, 4)
                            #endif
                        }
                    )
            }
        }
        .task(id: url) {
            await loadImage()
        }
    }

    private func loadImage() async {
        phase = .loading
        Self.logger.debug("Loading image: \(url.absoluteString)")

        do {
            let (data, response) = try await URLSession.shared.data(from: url)

            guard let http = response as? HTTPURLResponse else {
                let msg = "Non-HTTP response"
                Self.logger.error("\(msg) for \(url.absoluteString)")
                phase = .failure(msg)
                return
            }

            Self.logger.info("Image HTTP \(http.statusCode) for \(url.absoluteString) (\(data.count) bytes, type=\(http.mimeType ?? "nil"))")

            guard (200...299).contains(http.statusCode) else {
                let body = String(data: data.prefix(200), encoding: .utf8) ?? ""
                let msg = "HTTP \(http.statusCode)"
                Self.logger.error("\(msg): \(body)")
                phase = .failure(msg)
                return
            }

            guard let uiImage = UIImage(data: data) else {
                let msg = "Invalid image data (\(data.count)B, type=\(http.mimeType ?? "?"))"
                Self.logger.error("\(msg)")
                phase = .failure(msg)
                return
            }

            phase = .success(Image(uiImage: uiImage))
        } catch {
            let msg = error.localizedDescription
            Self.logger.error("Network error loading \(url.absoluteString): \(msg)")
            phase = .failure(msg)
        }
    }
}
