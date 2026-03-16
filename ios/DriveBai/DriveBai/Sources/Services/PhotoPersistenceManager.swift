import Foundation
import UIKit

/// Manages persistence of car photos to disk
/// Photos are stored in the app's documents directory organized by car ID
@MainActor
final class PhotoPersistenceManager: ObservableObject {
    static let shared = PhotoPersistenceManager()

    private let fileManager = FileManager.default
    private let photosDirectoryName = "CarPhotos"

    private init() {
        createPhotosDirectoryIfNeeded()
    }

    // MARK: - Directory Management

    private var photosDirectory: URL {
        let documentsPath = fileManager.urls(for: .documentDirectory, in: .userDomainMask)[0]
        return documentsPath.appendingPathComponent(photosDirectoryName)
    }

    private func carDirectory(for carId: UUID) -> URL {
        photosDirectory.appendingPathComponent(carId.uuidString)
    }

    private func createPhotosDirectoryIfNeeded() {
        let path = photosDirectory
        if !fileManager.fileExists(atPath: path.path) {
            try? fileManager.createDirectory(at: path, withIntermediateDirectories: true)
        }
    }

    private func createCarDirectoryIfNeeded(for carId: UUID) {
        let path = carDirectory(for: carId)
        if !fileManager.fileExists(atPath: path.path) {
            try? fileManager.createDirectory(at: path, withIntermediateDirectories: true)
        }
    }

    // MARK: - Photo Filename

    private func photoFilename(for slotType: PhotoSlotType) -> String {
        "\(slotType.rawValue).jpg"
    }

    private func photoURL(carId: UUID, slotType: PhotoSlotType) -> URL {
        carDirectory(for: carId).appendingPathComponent(photoFilename(for: slotType))
    }

    // MARK: - Save Photo

    /// Save photo data for a specific car and slot
    /// - Parameters:
    ///   - data: The image data (JPEG)
    ///   - carId: The car's UUID
    ///   - slotType: The photo slot type
    /// - Returns: The file URL where the photo was saved, or nil on failure
    @discardableResult
    func savePhoto(data: Data, carId: UUID, slotType: PhotoSlotType) -> URL? {
        createCarDirectoryIfNeeded(for: carId)
        let fileURL = photoURL(carId: carId, slotType: slotType)

        do {
            try data.write(to: fileURL)
            return fileURL
        } catch {
            print("Failed to save photo: \(error)")
            return nil
        }
    }

    /// Save UIImage for a specific car and slot (compresses to JPEG)
    /// - Parameters:
    ///   - image: The UIImage to save
    ///   - carId: The car's UUID
    ///   - slotType: The photo slot type
    ///   - compressionQuality: JPEG compression quality (0.0 to 1.0)
    /// - Returns: The file URL where the photo was saved, or nil on failure
    @discardableResult
    func savePhoto(image: UIImage, carId: UUID, slotType: PhotoSlotType, compressionQuality: CGFloat = 0.8) -> URL? {
        guard let data = image.jpegData(compressionQuality: compressionQuality) else {
            return nil
        }
        return savePhoto(data: data, carId: carId, slotType: slotType)
    }

    // MARK: - Load Photo

    /// Load photo data for a specific car and slot
    /// - Parameters:
    ///   - carId: The car's UUID
    ///   - slotType: The photo slot type
    /// - Returns: The image data, or nil if not found
    func loadPhotoData(carId: UUID, slotType: PhotoSlotType) -> Data? {
        let fileURL = photoURL(carId: carId, slotType: slotType)
        return try? Data(contentsOf: fileURL)
    }

    /// Load UIImage for a specific car and slot
    /// - Parameters:
    ///   - carId: The car's UUID
    ///   - slotType: The photo slot type
    /// - Returns: The UIImage, or nil if not found
    func loadPhoto(carId: UUID, slotType: PhotoSlotType) -> UIImage? {
        guard let data = loadPhotoData(carId: carId, slotType: slotType) else {
            return nil
        }
        return UIImage(data: data)
    }

    /// Check if a photo exists for a specific car and slot
    func photoExists(carId: UUID, slotType: PhotoSlotType) -> Bool {
        let fileURL = photoURL(carId: carId, slotType: slotType)
        return fileManager.fileExists(atPath: fileURL.path)
    }

    // MARK: - Delete Photo

    /// Delete a specific photo
    /// - Parameters:
    ///   - carId: The car's UUID
    ///   - slotType: The photo slot type
    func deletePhoto(carId: UUID, slotType: PhotoSlotType) {
        let fileURL = photoURL(carId: carId, slotType: slotType)
        try? fileManager.removeItem(at: fileURL)
    }

    /// Delete all photos for a car
    /// - Parameter carId: The car's UUID
    func deleteAllPhotos(for carId: UUID) {
        let directory = carDirectory(for: carId)
        try? fileManager.removeItem(at: directory)
    }

    // MARK: - Batch Operations

    /// Save all photo slots for a car
    /// - Parameters:
    ///   - photoSlots: Array of CarPhotoSlot with localImageData
    ///   - carId: The car's UUID
    func saveAllPhotos(photoSlots: [CarPhotoSlot], carId: UUID) {
        for slot in photoSlots {
            if let data = slot.localImageData {
                savePhoto(data: data, carId: carId, slotType: slot.slotType)
            }
        }
    }

    /// Load all photos for a car and update the photo slots
    /// - Parameters:
    ///   - photoSlots: Array of CarPhotoSlot to update
    ///   - carId: The car's UUID
    /// - Returns: Updated photo slots with loaded data
    func loadAllPhotos(for photoSlots: [CarPhotoSlot], carId: UUID) -> [CarPhotoSlot] {
        return photoSlots.map { slot in
            var updatedSlot = slot
            if let data = loadPhotoData(carId: carId, slotType: slot.slotType) {
                updatedSlot.localImageData = data
            }
            return updatedSlot
        }
    }

    // MARK: - Storage Info

    /// Get the total size of photos for a car in bytes
    func photosSize(for carId: UUID) -> Int64 {
        let directory = carDirectory(for: carId)
        guard let enumerator = fileManager.enumerator(at: directory, includingPropertiesForKeys: [.fileSizeKey]) else {
            return 0
        }

        var totalSize: Int64 = 0
        for case let fileURL as URL in enumerator {
            if let fileSize = try? fileURL.resourceValues(forKeys: [.fileSizeKey]).fileSize {
                totalSize += Int64(fileSize)
            }
        }
        return totalSize
    }

    /// Get formatted size string for a car's photos
    func formattedPhotosSize(for carId: UUID) -> String {
        let bytes = photosSize(for: carId)
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }
}
