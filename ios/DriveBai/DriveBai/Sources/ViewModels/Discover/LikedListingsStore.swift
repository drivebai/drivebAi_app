import Foundation
import SwiftUI

/// Store for tracking liked/favorited listings
/// Syncs with backend and persists across app restarts
@MainActor
final class LikedListingsStore: ObservableObject {
    static let shared = LikedListingsStore()

    /// Set of liked listing IDs
    @Published private(set) var likedIDs: Set<UUID> = []
    @Published private(set) var isLoading = false
    @Published var error: String?

    private let apiClient: APIClientProtocol

    private init(apiClient: APIClientProtocol = APIClient.shared) {
        self.apiClient = apiClient
    }

    // MARK: - Public API

    /// Check if a listing is liked
    func isLiked(_ id: UUID) -> Bool {
        likedIDs.contains(id)
    }

    /// Toggle liked state for a listing (with backend sync)
    func toggleLike(_ id: UUID) {
        if likedIDs.contains(id) {
            unlikeListing(id)
        } else {
            likeListing(id)
        }
    }

    /// Fetch liked listings from backend
    func fetchLikedListings() async {
        isLoading = true
        error = nil

        do {
            let ids = try await apiClient.fetchLikedListings()
            likedIDs = Set(ids)
            #if DEBUG
            print("[LikedListingsStore] Fetched \(ids.count) liked listings from server")
            #endif
        } catch {
            // Silently fail - likes will start fresh
            #if DEBUG
            print("[LikedListingsStore] Failed to fetch liked listings: \(error)")
            #endif
        }

        isLoading = false
    }

    /// Like a listing (optimistic update with backend sync)
    private func likeListing(_ id: UUID) {
        // Optimistic update
        likedIDs.insert(id)
        #if DEBUG
        print("[LikedListingsStore] Added like for listing: \(id)")
        #endif

        // Sync with backend
        Task {
            do {
                _ = try await apiClient.likeListing(id: id)
            } catch {
                // Revert on failure
                likedIDs.remove(id)
                self.error = "Failed to save like"
                #if DEBUG
                print("[LikedListingsStore] Failed to like listing: \(error)")
                #endif
            }
        }
    }

    /// Unlike a listing (optimistic update with backend sync)
    private func unlikeListing(_ id: UUID) {
        // Optimistic update
        likedIDs.remove(id)
        #if DEBUG
        print("[LikedListingsStore] Removed like for listing: \(id)")
        #endif

        // Sync with backend
        Task {
            do {
                _ = try await apiClient.unlikeListing(id: id)
            } catch {
                // Revert on failure
                likedIDs.insert(id)
                self.error = "Failed to remove like"
                #if DEBUG
                print("[LikedListingsStore] Failed to unlike listing: \(error)")
                #endif
            }
        }
    }

    /// Get count of liked listings
    var likedCount: Int {
        likedIDs.count
    }

    /// Clear all likes (e.g., on logout)
    func clearAll() {
        likedIDs.removeAll()
        error = nil
        #if DEBUG
        print("[LikedListingsStore] Cleared all liked listings")
        #endif
    }
}
