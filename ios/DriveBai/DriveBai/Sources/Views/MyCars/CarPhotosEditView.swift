import SwiftUI
import PhotosUI

/// Photo grid editor with labeled slots - pixel-clean layout
/// Layout: 2-column grid with 5 slots (Cover-Front, Right, Left, Back, Dashboard)
/// Dashboard slot is in left column with invisible spacer in right column
struct CarPhotosEditView: View {
    @Binding var photoSlots: [CarPhotoSlot]
    @Environment(\.dismiss) private var dismiss

    // Grid configuration
    private let columns = [
        GridItem(.flexible(), spacing: 16),
        GridItem(.flexible(), spacing: 16)
    ]
    private let horizontalPadding: CGFloat = 20
    private let tileSpacing: CGFloat = 16
    private let tileCornerRadius: CGFloat = 16
    private let tileAspectRatio: CGFloat = 1.4 // 16:10 ≈ 1.6, using 1.4 for slightly taller tiles

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 24) {
                    // Instructions
                    Text("Upload photos of your car from different angles")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 16)
                        .padding(.top, 8)

                    // Photo grid
                    LazyVGrid(columns: columns, spacing: tileSpacing) {
                        // First 4 slots in 2x2 grid
                        ForEach(sortedSlots.prefix(4)) { slot in
                            CarPhotoTileView(
                                slot: slot,
                                cornerRadius: tileCornerRadius,
                                aspectRatio: tileAspectRatio,
                                onPhotoSelected: { imageData in
                                    updateSlot(slot, with: imageData)
                                }
                            )
                        }

                        // 5th slot (Dashboard) - left column
                        if let dashboardSlot = sortedSlots.dropFirst(4).first {
                            CarPhotoTileView(
                                slot: dashboardSlot,
                                cornerRadius: tileCornerRadius,
                                aspectRatio: tileAspectRatio,
                                onPhotoSelected: { imageData in
                                    updateSlot(dashboardSlot, with: imageData)
                                }
                            )

                            // Invisible spacer tile in right column to maintain grid alignment
                            Color.clear
                                .aspectRatio(tileAspectRatio, contentMode: .fit)
                        }
                    }
                    .padding(.horizontal, horizontalPadding)

                    // Helper text
                    Text("Tap each slot to select a photo")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .padding(.bottom, 16)
                }
            }
            .background(Color(.systemBackground))
            .navigationTitle("Car photos")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") {
                        dismiss()
                    }
                }

                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Done") {
                        dismiss()
                    }
                    .fontWeight(.semibold)
                    .foregroundColor(Color.driveBaiPrimary)
                }
            }
        }
    }

    private var sortedSlots: [CarPhotoSlot] {
        photoSlots.sorted { $0.slotType.sortOrder < $1.slotType.sortOrder }
    }

    private func updateSlot(_ slot: CarPhotoSlot, with imageData: Data) {
        if let index = photoSlots.firstIndex(where: { $0.id == slot.id }) {
            print("[CarPhotosEditView] Photo selected for slot: \(slot.slotType.rawValue), size: \(imageData.count) bytes")
            photoSlots[index].localImageData = imageData
        }
    }
}

// MARK: - Reusable Photo Tile Component

/// A single photo tile with consistent sizing, corner radius, and styling
/// Handles both filled (with image) and empty (placeholder) states
private struct CarPhotoTileView: View {
    let slot: CarPhotoSlot
    let cornerRadius: CGFloat
    let aspectRatio: CGFloat
    let onPhotoSelected: (Data) -> Void

    // Per-tile picker state (prevents shared state bugs)
    @State private var pickerItem: PhotosPickerItem?
    @State private var isLoading: Bool = false

    var body: some View {
        VStack(spacing: 8) {
            // Photo tile with picker
            PhotosPicker(selection: $pickerItem, matching: .images) {
                tileContent
                    .aspectRatio(aspectRatio, contentMode: .fit)
                    .frame(maxWidth: .infinity)
                    .clipShape(RoundedRectangle(cornerRadius: cornerRadius))
                    .overlay(
                        RoundedRectangle(cornerRadius: cornerRadius)
                            .stroke(Color(.systemGray4), lineWidth: 1)
                    )
            }
            .buttonStyle(PlainButtonStyle())
            .onChange(of: pickerItem) { _, newItem in
                guard let newItem = newItem else { return }
                loadPhoto(from: newItem)
            }

            // Label with fixed height for alignment
            Text(slot.slotType.displayLabel)
                .font(.subheadline)
                .fontWeight(.semibold)
                .foregroundColor(.primary)
                .multilineTextAlignment(.center)
                .frame(height: 20)
                .frame(maxWidth: .infinity)
        }
    }

    @ViewBuilder
    private var tileContent: some View {
        GeometryReader { geometry in
            ZStack {
                if let data = slot.localImageData, let uiImage = UIImage(data: data) {
                    // Local image (newly selected)
                    Image(uiImage: uiImage)
                        .resizable()
                        .scaledToFill()
                        .frame(width: geometry.size.width, height: geometry.size.height)
                        .clipped()
                } else if let fullURL = slot.fullImageURL {
                    // Remote image from server
                    AsyncImage(url: fullURL) { phase in
                        switch phase {
                        case .empty:
                            loadingPlaceholder
                        case .success(let image):
                            image
                                .resizable()
                                .scaledToFill()
                                .frame(width: geometry.size.width, height: geometry.size.height)
                                .clipped()
                        case .failure:
                            emptyPlaceholder
                        @unknown default:
                            emptyPlaceholder
                        }
                    }
                } else {
                    // Empty state
                    emptyPlaceholder
                }

                // Loading overlay
                if isLoading {
                    Color.black.opacity(0.4)
                    ProgressView()
                        .tint(.white)
                        .scaleEffect(1.2)
                }
            }
        }
    }

    private var emptyPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray6))
            .overlay(
                VStack(spacing: 8) {
                    Image(systemName: "camera.fill")
                        .font(.system(size: 28))
                        .foregroundColor(Color(.systemGray3))

                    Image(systemName: "plus.circle.fill")
                        .font(.system(size: 18))
                        .foregroundColor(Color.driveBaiPrimary)
                }
            )
    }

    private var loadingPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray6))
            .overlay(
                ProgressView()
                    .tint(Color.driveBaiPrimary)
            )
    }

    private func loadPhoto(from item: PhotosPickerItem) {
        isLoading = true
        print("[CarPhotoTileView] Loading photo for slot: \(slot.slotType.rawValue)")

        Task {
            do {
                if let data = try await item.loadTransferable(type: Data.self) {
                    await MainActor.run {
                        print("[CarPhotoTileView] Loaded \(data.count) bytes for slot: \(slot.slotType.rawValue)")
                        onPhotoSelected(data)
                        isLoading = false
                    }
                } else {
                    print("[CarPhotoTileView] No data for slot: \(slot.slotType.rawValue)")
                    await MainActor.run {
                        isLoading = false
                    }
                }
            } catch {
                print("[CarPhotoTileView] Error loading photo: \(error.localizedDescription)")
                await MainActor.run {
                    isLoading = false
                }
            }
        }
    }
}

// MARK: - Preview

#Preview("Empty Slots") {
    CarPhotosEditView(
        photoSlots: .constant(Car.createEmptyPhotoSlots())
    )
}

#Preview("With Photos") {
    CarPhotosEditView(
        photoSlots: .constant(OwnerCarsStore.shared.cars.first?.photoSlots ?? Car.createEmptyPhotoSlots())
    )
}
