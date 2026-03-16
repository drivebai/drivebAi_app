import SwiftUI

// MARK: - Shared Photo Slot Display

/// Displays a car photo slot image (local data, server URL, or placeholder)
/// Shared across CarDetailEditView carousel, CarPhotosEditView grid, and other views
struct CarPhotoSlotImage: View {
    let slot: CarPhotoSlot
    var showLabel: Bool = false
    var cornerRadius: CGFloat = 12
    var contentMode: ContentMode = .fill

    var body: some View {
        ZStack {
            if let data = slot.localImageData, let uiImage = UIImage(data: data) {
                // Local image data (newly selected or cached)
                Image(uiImage: uiImage)
                    .resizable()
                    .aspectRatio(contentMode: contentMode)
            } else if let fullURL = slot.fullImageURL {
                // URL image from server — uses RemoteImage for HTTP-level error logging
                RemoteImage(url: fullURL, contentMode: contentMode)
            } else {
                // No image - show placeholder
                emptyPlaceholder
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: cornerRadius))
        .overlay(
            Group {
                if showLabel {
                    VStack {
                        Spacer()
                        Text(slot.slotType.displayLabel)
                            .font(.caption2)
                            .fontWeight(.medium)
                            .foregroundColor(.white)
                            .padding(.horizontal, 8)
                            .padding(.vertical, 4)
                            .background(Color.black.opacity(0.6))
                            .clipShape(RoundedRectangle(cornerRadius: 4))
                            .padding(8)
                    }
                }
            }
        )
    }

    private var emptyPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray6))
            .overlay(
                VStack(spacing: 8) {
                    Image(systemName: "camera.fill")
                        .font(.system(size: 28))
                        .foregroundColor(.secondary)

                    Image(systemName: "plus.circle.fill")
                        .font(.system(size: 20))
                        .foregroundColor(Color.driveBaiPrimary)
                }
            )
    }

    private var loadingPlaceholder: some View {
        Rectangle()
            .fill(Color.driveBaiPrimary.opacity(0.1))
            .overlay(ProgressView())
    }

    private var errorPlaceholder: some View {
        Rectangle()
            .fill(Color.driveBaiPrimary.opacity(0.15))
            .overlay(
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 24))
                    .foregroundColor(Color.driveBaiPrimary.opacity(0.5))
            )
    }
}

// MARK: - Photo Carousel for Edit Car Screen

/// Swipeable carousel showing car photos with page indicators
struct CarPhotoCarousel: View {
    let photoSlots: [CarPhotoSlot]
    let onEditPhotos: () -> Void

    @State private var currentIndex: Int = 0

    private var sortedSlots: [CarPhotoSlot] {
        photoSlots
            .filter { $0.hasImage }
            .sorted { $0.slotType.sortOrder < $1.slotType.sortOrder }
    }

    private var slotsToShow: [CarPhotoSlot] {
        // Show slots with images, or all slots if none have images
        sortedSlots.isEmpty ? photoSlots.sorted { $0.slotType.sortOrder < $1.slotType.sortOrder } : sortedSlots
    }

    var body: some View {
        VStack(spacing: 0) {
            ZStack(alignment: .bottomTrailing) {
                // Photo carousel
                TabView(selection: $currentIndex) {
                    ForEach(Array(slotsToShow.enumerated()), id: \.element.id) { index, slot in
                        CarPhotoSlotImage(
                            slot: slot,
                            showLabel: true,
                            cornerRadius: 0,
                            contentMode: .fill
                        )
                        .tag(index)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .never))
                .frame(height: 240)

                // Overlay controls
                VStack {
                    Spacer()
                    HStack {
                        // Page indicator
                        HStack(spacing: 6) {
                            ForEach(0..<slotsToShow.count, id: \.self) { index in
                                Circle()
                                    .fill(index == currentIndex ? Color.white : Color.white.opacity(0.5))
                                    .frame(width: 8, height: 8)
                            }
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 6)
                        .background(Color.black.opacity(0.4))
                        .clipShape(Capsule())

                        Spacer()

                        // Edit photos button
                        Button(action: onEditPhotos) {
                            HStack(spacing: 6) {
                                Image(systemName: "photo.on.rectangle")
                                    .font(.caption)
                                Text("Edit photos")
                                    .font(.caption)
                                    .fontWeight(.medium)
                            }
                            .foregroundColor(.white)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 8)
                            .background(Color.driveBaiPrimary)
                            .cornerRadius(8)
                        }
                    }
                    .padding(12)
                }
            }
        }
    }
}

// MARK: - Photo Grid Thumbnail

/// Small thumbnail for photo grids (used in CarPhotosEditView)
struct CarPhotoThumbnail: View {
    let slot: CarPhotoSlot
    var size: CGFloat = 140

    var body: some View {
        CarPhotoSlotImage(
            slot: slot,
            showLabel: false,
            cornerRadius: 12,
            contentMode: .fill
        )
        .frame(height: size)
        .clipped()
    }
}

// MARK: - Preview

#Preview("Carousel") {
    CarPhotoCarousel(
        photoSlots: Car.createEmptyPhotoSlots(),
        onEditPhotos: {}
    )
}

#Preview("Slot Image - Empty") {
    CarPhotoSlotImage(
        slot: CarPhotoSlot(slotType: .coverFront),
        showLabel: true
    )
    .frame(height: 200)
    .padding()
}
