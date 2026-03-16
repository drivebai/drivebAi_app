import SwiftUI

/// Card displaying a listing with image, metadata - Figma accurate styling
struct ListingCard: View {
    let listing: ListingSummary
    var onTap: (() -> Void)? = nil
    var onChatTap: (() -> Void)? = nil
    var onOptionSelect: ((Int) -> Void)? = nil

    var body: some View {
        Button(action: { onTap?() }) {
            cardContent
        }
        .buttonStyle(PlainButtonStyle())
    }

    /// Image view that shows the car photo or a placeholder
    @ViewBuilder
    private var carImageView: some View {
        if let imageURL = listing.imageURL, let fullURL = ImageURLHelper.fullURL(for: imageURL) {
            RemoteImage(url: fullURL, contentMode: .fill)
        } else {
            // No image URL - show placeholder
            placeholderView
        }
    }

    private var placeholderView: some View {
        Rectangle()
            .fill(TodayLayout.tealAccentLight)
            .overlay(
                Image(systemName: "car.fill")
                    .font(.system(size: 36))
                    .foregroundColor(TodayLayout.tealAccent.opacity(0.4))
            )
    }

    private var cardContent: some View {
        VStack(alignment: .leading, spacing: TodayLayout.contentSpacing) {
            // Image with status chip and action icon
            ZStack(alignment: .topLeading) {
                // Car image or placeholder
                carImageView
                    .frame(height: 120)
                    .cornerRadius(12)

                // Status chip - top left
                CarStatusChip()
                    .padding(8)

                // Action icon - top right
                VStack {
                    HStack {
                        Spacer()
                        Button(action: {
                            onChatTap?()
                        }) {
                            Image(systemName: "arrow.up.right")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundColor(.white)
                                .padding(6)
                                .background(Color.black.opacity(0.4))
                                .clipShape(Circle())
                        }
                        .padding(8)
                    }
                    Spacer()
                }
            }

            // Title
            Text(listing.title)
                .font(.subheadline)
                .fontWeight(.semibold)
                .foregroundColor(.primary)
                .lineLimit(1)

            // Metadata row - compact style
            HStack(spacing: 0) {
                MetadataItem(label: "Weekly", value: String(format: "$%.0f", listing.weeklyPrice))
                Spacer()
                MetadataItem(label: "Rented", value: "\(listing.rentedWeeks)w")
                Spacer()
                MetadataItem(label: "Total", value: String(format: "$%.0f", listing.totalEarned))
            }
        }
        .padding(TodayLayout.contentSpacing)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: TodayLayout.cardBorderWidth)
        )
    }
}

/// Car status chip matching Figma - white pill with checkmark
private struct CarStatusChip: View {
    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: "checkmark")
                .font(.system(size: 10, weight: .semibold))
            Text("Car status")
                .font(.caption2)
                .fontWeight(.medium)
        }
        .foregroundColor(.primary)
        .padding(.horizontal, 8)
        .padding(.vertical, 5)
        .background(Color(.systemBackground))
        .cornerRadius(4)
    }
}

/// Small metadata item with label and value - compact styling
private struct MetadataItem: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption2)
                .foregroundColor(.secondary)
                .lineLimit(1)

            Text(value)
                .font(.caption)
                .fontWeight(.semibold)
                .foregroundColor(.primary)
                .lineLimit(1)
        }
    }
}

#Preview {
    ScrollView(.horizontal, showsIndicators: false) {
        HStack(spacing: 16) {
            ListingCard(listing: ListingSummary.mockListings()[0])
                .frame(width: 280)

            ListingCard(listing: ListingSummary.mockListings()[1])
                .frame(width: 280)
        }
        .padding()
    }
    .background(Color(.systemBackground))
}
