import SwiftUI

/// Layout constants for Today tab views to ensure consistent spacing and styling
enum TodayLayout {
    // MARK: - Spacing
    static let horizontalPadding: CGFloat = 16
    static let sectionSpacing: CGFloat = 24
    static let cardSpacing: CGFloat = 16
    static let headerSpacing: CGFloat = 12
    static let contentSpacing: CGFloat = 12

    // MARK: - Card Styling
    static let cardCornerRadius: CGFloat = 16
    static let cardBorderWidth: CGFloat = 1
    static let cardBorderColor = Color(.systemGray4)
    static let cardBackgroundColor = Color(.systemBackground)

    // MARK: - Colors
    static let pageBackground = Color(.systemBackground)
    static let tealAccent = Color.driveBaiPrimary
    static let tealAccentLight = Color.driveBaiPrimary.opacity(0.08)
    static let tealAccentMedium = Color.driveBaiPrimary.opacity(0.15)

    // MARK: - Header
    static let headerTitleFont = Font.system(size: 34, weight: .bold)
    static let sectionTitleFont = Font.system(size: 17, weight: .semibold)
    static let helperTextFont = Font.subheadline

    // MARK: - Active Listing Card (horizontal scroll row)
    static let activeListingCardWidth: CGFloat = 280
    static let activeListingCardHeight: CGFloat = 220

    // MARK: - Button Heights
    static let optionButtonHeight: CGFloat = 40
    static let optionButtonMinWidth: CGFloat = 80
}

/// Custom header for Today screen matching Figma design
struct TodayHeaderView: View {
    let title: String
    let unreadCount: Int
    let onBellTap: () -> Void

    var body: some View {
        HStack(alignment: .center) {
            Text(title)
                .font(TodayLayout.headerTitleFont)
                .foregroundColor(.primary)

            Spacer()

            Button(action: onBellTap) {
                ZStack(alignment: .topTrailing) {
                    Image(systemName: "bell.fill")
                        .font(.system(size: 20))
                        .foregroundColor(.primary)

                    if unreadCount > 0 {
                        Circle()
                            .fill(Color.red)
                            .frame(width: 10, height: 10)
                            .offset(x: 3, y: -3)
                    }
                }
            }
        }
        .padding(.horizontal, TodayLayout.horizontalPadding)
        .padding(.top, 8)
        .padding(.bottom, TodayLayout.headerSpacing)
    }
}

#Preview {
    VStack {
        TodayHeaderView(title: "Today", unreadCount: 3, onBellTap: {})
        TodayHeaderView(title: "Today", unreadCount: 0, onBellTap: {})
    }
}
