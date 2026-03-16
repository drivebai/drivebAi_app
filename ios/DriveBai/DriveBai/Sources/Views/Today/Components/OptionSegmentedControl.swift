import SwiftUI

/// A segmented control with options - Figma accurate with no text wrapping
struct OptionSegmentedControl: View {
    let options: [String]
    @Binding var selectedIndex: Int?
    var onSelect: ((Int) -> Void)?

    var body: some View {
        HStack(spacing: 8) {
            ForEach(Array(options.enumerated()), id: \.offset) { index, option in
                OptionButton(
                    title: option,
                    isSelected: selectedIndex == index,
                    action: {
                        selectedIndex = index
                        onSelect?(index)
                    }
                )
            }
        }
    }
}

/// Individual option button - fixed height, no text wrapping
private struct OptionButton: View {
    let title: String
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 14, weight: isSelected ? .semibold : .regular))
                .foregroundColor(isSelected ? .white : .primary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
                .allowsTightening(true)
                .frame(maxWidth: .infinity)
                .frame(height: TodayLayout.optionButtonHeight)
                .background(
                    RoundedRectangle(cornerRadius: 8)
                        .fill(isSelected ? TodayLayout.tealAccent : Color(.systemGray6))
                )
        }
        .buttonStyle(PlainButtonStyle())
    }
}

#Preview {
    VStack(spacing: 20) {
        OptionSegmentedControl(
            options: ["Option 1", "Option 2", "Option 3"],
            selectedIndex: .constant(0)
        )

        OptionSegmentedControl(
            options: ["Upload Now", "Remind Later", "Skip"],
            selectedIndex: .constant(1)
        )

        OptionSegmentedControl(
            options: ["Yes", "No", "Maybe"],
            selectedIndex: .constant(nil)
        )
    }
    .padding()
    .background(Color(.systemBackground))
}
