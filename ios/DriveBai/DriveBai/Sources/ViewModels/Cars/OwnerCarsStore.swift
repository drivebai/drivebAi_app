import Foundation
import SwiftUI

/// Shared store for managing owner's cars - backend-backed
@MainActor
final class OwnerCarsStore: ObservableObject {
    static let shared = OwnerCarsStore()

    @Published var cars: [Car] = []
    @Published var isLoading: Bool = false
    @Published var error: String?
    /// Machine-readable code of the last API failure (e.g.
    /// "CAR_CURRENTLY_RENTED", "CAR_HAS_ACTIVE_OBLIGATIONS",
    /// "SALE_REQUIREMENTS_NOT_MET"). Set alongside `error` so views can
    /// branch on the code instead of string-matching the human message
    /// (QA pts 8/9). Nil for non-server failures.
    @Published var errorCode: String?

    private let apiClient: APIClient

    // MARK: - Computed Properties

    var carsForRent: [Car] {
        cars.filter { $0.isForRent && !$0.isPaused }
    }

    var carsForSale: [Car] {
        cars.filter { $0.isForSale && !$0.isPaused }
    }

    var forRentCount: Int {
        carsForRent.count
    }

    var forSaleCount: Int {
        carsForSale.count
    }

    // MARK: - Init

    private init(apiClient: APIClient = .shared) {
        self.apiClient = apiClient
    }

    // MARK: - Fetch Cars from Backend

    func fetchCars() async {
        isLoading = true
        error = nil

        #if DEBUG
        print("[OwnerCarsStore] fetchCars called")
        #endif

        do {
            cars = try await apiClient.fetchCars()

            #if DEBUG
            print("[OwnerCarsStore] Fetched \(cars.count) cars:")
            for car in cars {
                let coverSlot = car.photoSlots.first { $0.slotType == .coverFront }
                print("  - \(car.id): '\(car.title)' status=\(car.status.rawValue) coverURL=\(coverSlot?.imageURL ?? "nil")")
            }
            #endif
        } catch let apiError as APIError {
            error = apiError.errorDescription
            print("[OwnerCarsStore] Failed to fetch cars: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to fetch cars: \(error)")
        }

        isLoading = false
    }

    // MARK: - CRUD Operations

    func addCar(_ car: Car) async -> Car? {
        isLoading = true
        error = nil
        errorCode = nil

        do {
            let request = car.toCreateRequest()

            // DEBUG: Log the request being sent
            #if DEBUG
            print("[OwnerCarsStore] Creating car with request:")
            print("  - make: \(request.make)")
            print("  - model: \(request.model)")
            print("  - year: \(request.year)")
            print("  - isForRent: \(request.isForRent)")
            print("  - isForSale: \(request.isForSale)")
            if let encoder = try? JSONEncoder().encode(request),
               let jsonStr = String(data: encoder, encoding: .utf8) {
                print("[OwnerCarsStore] Request JSON: \(jsonStr)")
            }
            #endif

            let createdCar = try await apiClient.createCar(request: request)
            print("[OwnerCarsStore] Car created successfully with ID: \(createdCar.id)")
            cars.append(createdCar)
            isLoading = false
            return createdCar
        } catch let apiError as APIError {
            // Preserve the machine code so the wizard can branch (INVALID_VIN
            // / duplicate) without string-matching the human message.
            errorCode = apiError.errorCode
            // Surface clean, fixable copy for the VIN-specific rejections so
            // the wizard can show them inline on the VIN row. The duplicate
            // 409 arrives as a flat `{"error":"vin_already_in_use", ...}` body;
            // the malformed-VIN 400 arrives as the nested envelope with code
            // "INVALID_VIN" (W1). Anything else keeps the server message.
            if case .serverError(let code, _) = apiError {
                switch code {
                case "vin_already_in_use":
                    error = "VIN already in use"
                case "INVALID_VIN":
                    error = "That VIN isn't valid. Please check all 17 characters."
                default:
                    error = apiError.errorDescription
                }
            } else {
                error = apiError.errorDescription
            }
            print("[OwnerCarsStore] Failed to create car - APIError: \(apiError)")
            print("[OwnerCarsStore] Error description: \(apiError.errorDescription ?? "nil")")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to create car - Error: \(error)")
        }

        isLoading = false
        return nil
    }

    func updateCar(_ car: Car) async -> Car? {
        error = nil
        errorCode = nil

        do {
            let request = car.toUpdateRequest()
            let updatedCar = try await apiClient.updateCar(id: car.id, request: request)
            if let index = cars.firstIndex(where: { $0.id == car.id }) {
                cars[index] = updatedCar
            }
            return updatedCar
        } catch let apiError as APIError {
            error = apiError.errorDescription
            errorCode = apiError.errorCode
            print("[OwnerCarsStore] Failed to update car: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to update car: \(error)")
        }

        return nil
    }

    func updateCarLocation(carId: UUID, request: UpdateCarLocationRequest) async -> Car? {
        error = nil

        #if DEBUG
        print("[OwnerCarsStore] updateCarLocation carId=\(carId) lat=\(request.latitude) lng=\(request.longitude)")
        #endif

        do {
            let updatedCar = try await apiClient.updateCarLocation(carId: carId, request: request)
            if let index = cars.firstIndex(where: { $0.id == carId }) {
                cars[index] = updatedCar
            }
            return updatedCar
        } catch let apiError as APIError {
            error = apiError.errorDescription
            print("[OwnerCarsStore] Failed to update car location: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to update car location: \(error)")
        }

        return nil
    }

    func deleteCar(id: UUID) async -> Bool {
        error = nil
        errorCode = nil

        do {
            _ = try await apiClient.deleteCar(id: id)
            cars.removeAll { $0.id == id }
            // Also clean up local photo cache
            PhotoPersistenceManager.shared.deleteAllPhotos(for: id)
            return true
        } catch let apiError as APIError {
            error = apiError.errorDescription
            errorCode = apiError.errorCode
            print("[OwnerCarsStore] Failed to delete car: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to delete car: \(error)")
        }

        return false
    }

    func togglePaused(id: UUID) async -> Bool {
        error = nil
        errorCode = nil

        do {
            let updatedCar = try await apiClient.togglePauseCar(id: id)
            if let index = cars.firstIndex(where: { $0.id == id }) {
                cars[index] = updatedCar
            }
            return true
        } catch let apiError as APIError {
            error = apiError.errorDescription
            errorCode = apiError.errorCode
            print("[OwnerCarsStore] Failed to toggle pause: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to toggle pause: \(error)")
        }

        return false
    }

    func getCar(id: UUID) -> Car? {
        cars.first { $0.id == id }
    }

    // MARK: - Photo Operations

    /// Upload photo to backend for a specific car slot
    func uploadPhoto(data: Data, carId: UUID, slotType: PhotoSlotType) async -> Bool {
        error = nil

        #if DEBUG
        print("[OwnerCarsStore] Uploading photo:")
        print("  - carId: \(carId)")
        print("  - slotType: \(slotType.rawValue)")
        print("  - data size: \(data.count) bytes")
        #endif

        do {
            let filename = "\(slotType.rawValue).jpg"
            let response = try await apiClient.uploadCarPhoto(
                carId: carId,
                slotType: slotType,
                fileData: data,
                filename: filename,
                mimeType: "image/jpeg"
            )

            print("[OwnerCarsStore] Photo uploaded successfully:")
            print("  - photoId: \(response.id)")
            print("  - fileUrl: \(response.fileUrl)")

            // Update local state
            if let carIndex = cars.firstIndex(where: { $0.id == carId }),
               let slotIndex = cars[carIndex].photoSlots.firstIndex(where: { $0.slotType == slotType }) {
                cars[carIndex].photoSlots[slotIndex].imageURL = response.fileUrl
                cars[carIndex].photoSlots[slotIndex].localImageData = data
                cars[carIndex].updatedAt = Date()
                print("[OwnerCarsStore] Updated local car state with photo URL")
            } else {
                print("[OwnerCarsStore] Warning: Could not find car or slot to update locally")
            }

            // Also save to local cache for offline viewing
            PhotoPersistenceManager.shared.savePhoto(data: data, carId: carId, slotType: slotType)

            return true
        } catch let apiError as APIError {
            error = apiError.errorDescription
            print("[OwnerCarsStore] Failed to upload photo - APIError: \(apiError)")
            print("[OwnerCarsStore] Error description: \(apiError.errorDescription ?? "nil")")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to upload photo - Error: \(error)")
        }

        return false
    }

    /// Delete photo from backend for a specific car slot
    func deletePhoto(carId: UUID, photoId: UUID, slotType: PhotoSlotType) async -> Bool {
        error = nil

        do {
            _ = try await apiClient.deleteCarPhoto(carId: carId, photoId: photoId)

            // Update local state
            if let carIndex = cars.firstIndex(where: { $0.id == carId }),
               let slotIndex = cars[carIndex].photoSlots.firstIndex(where: { $0.slotType == slotType }) {
                cars[carIndex].photoSlots[slotIndex].imageURL = nil
                cars[carIndex].photoSlots[slotIndex].localImageData = nil
                cars[carIndex].updatedAt = Date()
            }

            // Also remove from local cache
            PhotoPersistenceManager.shared.deletePhoto(carId: carId, slotType: slotType)

            return true
        } catch let apiError as APIError {
            error = apiError.errorDescription
            print("[OwnerCarsStore] Failed to delete photo: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to delete photo: \(error)")
        }

        return false
    }

    // MARK: - Document Operations

    /// Upload document to backend for a car
    func uploadDocument(carId: UUID, documentType: CarDocumentType, data: Data, filename: String, mimeType: String) async -> CarDocumentAPIResponse? {
        error = nil

        do {
            let response = try await apiClient.uploadCarDocument(
                carId: carId,
                documentType: documentType,
                fileData: data,
                filename: filename,
                mimeType: mimeType
            )

            // Refresh car to get updated documents
            await refreshCar(id: carId)

            return response
        } catch let apiError as APIError {
            error = apiError.errorDescription
            print("[OwnerCarsStore] Failed to upload document: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to upload document: \(error)")
        }

        return nil
    }

    /// Delete document from backend
    func deleteDocument(carId: UUID, documentId: UUID) async -> Bool {
        error = nil
        errorCode = nil

        do {
            _ = try await apiClient.deleteCarDocument(carId: carId, documentId: documentId)

            // Update local state
            if let carIndex = cars.firstIndex(where: { $0.id == carId }) {
                cars[carIndex].documents.removeAll { $0.id == documentId }
                cars[carIndex].updatedAt = Date()
            }

            return true
        } catch let apiError as APIError {
            error = apiError.errorDescription
            errorCode = apiError.errorCode
            print("[OwnerCarsStore] Failed to delete document: \(apiError)")
        } catch {
            self.error = error.localizedDescription
            print("[OwnerCarsStore] Failed to delete document: \(error)")
        }

        return false
    }

    // MARK: - Refresh

    func refresh() async {
        await fetchCars()
    }

    // MARK: - Clear (for logout)

    /// Clear all cached data - called on logout to ensure next user gets fresh data
    func clearAll() {
        cars = []
        error = nil
        errorCode = nil
        isLoading = false
    }

    /// Refresh a single car from the backend
    func refreshCar(id: UUID) async {
        #if DEBUG
        print("[OwnerCarsStore] refreshCar called for id: \(id)")
        #endif

        do {
            let updatedCar = try await apiClient.getCar(id: id)

            #if DEBUG
            print("[OwnerCarsStore] Got updated car from API:")
            print("  - id: \(updatedCar.id)")
            print("  - title: \(updatedCar.title)")
            print("  - status: \(updatedCar.status.rawValue)")
            print("  - photoSlots count: \(updatedCar.photoSlots.count)")
            for slot in updatedCar.photoSlots {
                if let url = slot.imageURL {
                    print("  - slot \(slot.slotType.rawValue): imageURL=\(url)")
                }
            }
            #endif

            if let index = cars.firstIndex(where: { $0.id == id }) {
                #if DEBUG
                print("[OwnerCarsStore] Updating car at index \(index)")
                #endif
                cars[index] = updatedCar
            } else {
                #if DEBUG
                print("[OwnerCarsStore] WARNING: Car not found in local array, appending")
                #endif
                cars.append(updatedCar)
            }

            #if DEBUG
            print("[OwnerCarsStore] refreshCar complete. Total cars: \(cars.count)")
            #endif
        } catch {
            print("[OwnerCarsStore] Failed to refresh car: \(error)")
        }
    }

    // MARK: - Local Operations (for optimistic updates)

    /// Update car locally without API call (for optimistic UI updates)
    func updateCarLocally(_ car: Car) {
        if let index = cars.firstIndex(where: { $0.id == car.id }) {
            cars[index] = car
        }
    }

    /// Save photo locally for preview before upload
    func savePhotoLocally(data: Data, carId: UUID, slotType: PhotoSlotType) {
        if let carIndex = cars.firstIndex(where: { $0.id == carId }),
           let slotIndex = cars[carIndex].photoSlots.firstIndex(where: { $0.slotType == slotType }) {
            cars[carIndex].photoSlots[slotIndex].localImageData = data
            cars[carIndex].updatedAt = Date()
        }
        // Save to disk for persistence
        PhotoPersistenceManager.shared.savePhoto(data: data, carId: carId, slotType: slotType)
    }

    /// Delete photo locally
    func deletePhotoLocally(carId: UUID, slotType: PhotoSlotType) {
        if let carIndex = cars.firstIndex(where: { $0.id == carId }),
           let slotIndex = cars[carIndex].photoSlots.firstIndex(where: { $0.slotType == slotType }) {
            cars[carIndex].photoSlots[slotIndex].localImageData = nil
            cars[carIndex].photoSlots[slotIndex].imageURL = nil
            cars[carIndex].updatedAt = Date()
        }
        PhotoPersistenceManager.shared.deletePhoto(carId: carId, slotType: slotType)
    }

    /// Load all photos from disk for a car (for offline support)
    func loadPhotosFromDisk(for carId: UUID) {
        guard let carIndex = cars.firstIndex(where: { $0.id == carId }) else { return }
        cars[carIndex].photoSlots = PhotoPersistenceManager.shared.loadAllPhotos(
            for: cars[carIndex].photoSlots,
            carId: carId
        )
    }
}
