import SwiftUI

/// Empty state card with dashed teal border and light teal fill - Figma accurate
struct EmptyListingCard: View {
    let isOwner: Bool
    var onTap: (() -> Void)?

    private var promptText: String {
        isOwner ? "Rent or sell your first vehicle!" : "Discover your rental!"
    }

    private var iconName: String {
        isOwner ? "plus" : "arrow.up.right.square"
    }

    var body: some View {
        Button(action: {
            onTap?()
        }) {
            VStack(spacing: 12) {
                // Icon - teal colored
                Image(systemName: iconName)
                    .font(.system(size: 32, weight: .medium))
                    .foregroundColor(TodayLayout.tealAccent)

                // Prompt text - centered, semibold
                Text(promptText)
                    .font(.subheadline)
                    .fontWeight(.medium)
                    .foregroundColor(.primary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
            .frame(height: 180)
            .background(TodayLayout.tealAccentLight)
            .overlay(
                RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                    .strokeBorder(
                        style: StrokeStyle(lineWidth: 2, dash: [8, 4])
                    )
                    .foregroundColor(TodayLayout.tealAccent.opacity(0.5))
            )
            .cornerRadius(TodayLayout.cardCornerRadius)
        }
        .buttonStyle(PlainButtonStyle())
    }
}

#Preview {
    VStack(spacing: 20) {
        EmptyListingCard(isOwner: true)
        EmptyListingCard(isOwner: false)
    }
    .padding()
    .background(Color(.systemBackground))
}
