import Foundation
import Photos

/// Saves image data to the user's Photos library.
/// Uses `.addOnly` authorization (iOS 14+) — least-privilege, doesn't require
/// read access. The image is added as a photo asset via `PHAssetCreationRequest`
/// so the original format (JPEG/PNG/HEIC) is preserved.
enum PhotoLibrarySaver {
    enum SaveError: LocalizedError {
        case notAuthorized
        case writeFailed(String)

        var errorDescription: String? {
            switch self {
            case .notAuthorized:
                return "DriveBai doesn't have permission to save to your photo library. Enable it in Settings → Photos."
            case .writeFailed(let reason):
                return "Couldn't save the image: \(reason)"
            }
        }
    }

    /// Persist the given image data to the Photos library. Throws on permission
    /// denial or write failure.
    static func save(data: Data) async throws {
        let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
        guard status == .authorized || status == .limited else {
            throw SaveError.notAuthorized
        }

        do {
            try await PHPhotoLibrary.shared().performChanges {
                let request = PHAssetCreationRequest.forAsset()
                request.addResource(with: .photo, data: data, options: nil)
            }
        } catch {
            throw SaveError.writeFailed(error.localizedDescription)
        }
    }
}
