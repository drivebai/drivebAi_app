import SwiftUI

// MARK: - Price Editor Sheet

/// A reusable bottom-sheet price editor with a single unified surface:
/// a large centered price that can be nudged with -/+ buttons (±`step`
/// per tap) or tapped to type an exact amount on the decimal pad.
///
/// Bind to a `Double` (the underlying price amount). The -/+ buttons and
/// the text field edit the same bound value and stay synchronized.
/// Enforces `minValue` (default 0) — the price can never go below it.
struct PriceEditorSheet: View {
    let title: String
    @Binding var value: Double
    var minValue: Double = 0
    var step: Double = 10
    var subtitle: String? = nil

    @Environment(\.dismiss) private var dismiss
    @State private var textValue: String = ""
    @FocusState private var isTextFieldFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            // Top title pill
            HStack {
                Spacer()
                HStack(spacing: 6) {
                    Text(title)
                        .font(.subheadline)
                        .fontWeight(.medium)
                        .foregroundColor(.primary)
                    Image(systemName: "chevron.down")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(Color(.systemBackground))
                .overlay(
                    Capsule().stroke(Color.gray.opacity(0.25), lineWidth: 1)
                )
                .clipShape(Capsule())
                Spacer()
            }
            .padding(.top, 12)
            .padding(.bottom, 16)

            editorView
                .frame(maxWidth: .infinity)
                .padding(.horizontal, 20)

            Spacer(minLength: 20)

            // Primary CTA
            Button(action: commitAndDismiss) {
                Text("Done")
            }
            .buttonStyle(DriveBaiButtonStyle())
            .padding(.horizontal, 20)
            .padding(.bottom, 24)
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .onAppear {
            textValue = fieldText(for: max(value, minValue))
        }
        .onChange(of: isTextFieldFocused) { _, focused in
            if !focused {
                commitTextEntry()
            }
        }
    }

    // MARK: - Unified editor

    private var editorView: some View {
        VStack(spacing: 20) {
            Spacer(minLength: 0)

            VStack(spacing: 6) {
                priceDisplay

                if let subtitle {
                    Text(subtitle)
                        .font(.footnote)
                        .foregroundColor(.orange)
                }
            }

            // Minus / plus controls
            HStack(spacing: 40) {
                stepButton(systemImage: "minus", action: decrement)
                    .disabled(value <= minValue)

                Text(stepLabel)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .frame(minWidth: 80)

                stepButton(systemImage: "plus", action: increment)
            }
            .padding(.top, 8)

            Spacer(minLength: 0)
        }
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button("Done") { isTextFieldFocused = false }
                    .fontWeight(.semibold)
            }
        }
    }

    /// Big centered price. Shows a formatted, animated `Text` when idle
    /// (preserving the rolling-digit -/+ animation) and swaps to a raw
    /// decimal-pad `TextField` while the user is typing, so live input is
    /// never reformatted mid-keystroke. The canonical formatting is
    /// reapplied when the field loses focus (commit on unfocus).
    private var priceDisplay: some View {
        ZStack {
            // Editable field — visible while typing
            HStack(spacing: 4) {
                Text("$")
                    .font(.system(size: 44, weight: .bold))
                    .foregroundColor(.secondary)
                TextField("0", text: $textValue)
                    .font(.system(size: 56, weight: .bold))
                    .keyboardType(.decimalPad)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: true, vertical: false)
                    .focused($isTextFieldFocused)
                    .onChange(of: textValue) { _, newText in
                        let sanitized = sanitize(newText)
                        if sanitized != newText { textValue = sanitized }
                        if let parsed = Double(sanitized) {
                            value = max(parsed, minValue)
                        } else if sanitized.isEmpty {
                            value = minValue
                        }
                    }
            }
            .opacity(isTextFieldFocused ? 1 : 0)
            .allowsHitTesting(isTextFieldFocused)

            // Formatted display — visible when not typing
            if !isTextFieldFocused {
                Text(formattedValue)
                    .font(.system(size: 56, weight: .bold))
                    .foregroundColor(.primary)
                    .contentTransition(.numericText())
                    .animation(.easeOut(duration: 0.15), value: value)
                    .onTapGesture {
                        textValue = fieldText(for: value)
                        isTextFieldFocused = true
                    }
            }
        }
    }

    private var stepLabel: String {
        "±$\(Int(step))"
    }

    private func stepButton(systemImage: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 20, weight: .semibold))
                .foregroundColor(.primary)
                .frame(width: 54, height: 54)
                .background(Color(.systemBackground))
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .stroke(Color.gray.opacity(0.3), lineWidth: 1)
                )
                .clipShape(RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
    }

    // MARK: - Helpers

    private var formattedValue: String {
        Money(amount: value).formatted
    }

    /// Raw text shown inside the text field: plain digits with an optional
    /// decimal part — no "$" or thousands separators, which would fight the
    /// input filter and jump the cursor while typing.
    private func fieldText(for amount: Double) -> String {
        if amount.truncatingRemainder(dividingBy: 1) == 0 {
            return String(Int(amount))
        }
        return String(format: "%.2f", amount)
    }

    /// Keeps only digits and at most one decimal point (max 2 decimals).
    /// The decimal pad shows the LOCALE's separator — a comma on FR/DE/ES-
    /// region devices — so commas are treated as decimal points rather than
    /// stripped. Stripping made "12,50" silently become "1250": a 100×
    /// price error committed straight to the binding.
    private func sanitize(_ text: String) -> String {
        var seenDot = false
        var result = ""
        for character in text {
            if character.isNumber {
                result.append(character)
            } else if (character == "." || character == ","), !seenDot {
                seenDot = true
                result.append(".")
            }
        }
        if let dotIndex = result.firstIndex(of: "."),
           result.distance(from: dotIndex, to: result.endIndex) > 3 {
            result = String(result[..<result.index(dotIndex, offsetBy: 3)])
        }
        return result
    }

    private func increment() {
        value += step
        textValue = fieldText(for: value)
    }

    private func decrement() {
        value = max(minValue, value - step)
        textValue = fieldText(for: value)
    }

    /// Flushes the typed text into `value` (clamped to `minValue`) and
    /// normalizes the field text back to its canonical form. Runs whenever
    /// the field loses focus.
    private func commitTextEntry() {
        if let parsed = Double(textValue) {
            value = max(parsed, minValue)
        } else {
            value = max(value, minValue)
        }
        textValue = fieldText(for: value)
    }

    private func commitAndDismiss() {
        isTextFieldFocused = false
        // Ensure text edits are flushed into value before closing
        commitTextEntry()
        dismiss()
    }
}

// MARK: - Price Editor Row

/// Tappable row that displays a price and opens a `PriceEditorSheet` when tapped.
/// Designed to replace inline sliders/text fields with minimal layout change.
struct PriceEditorRow: View {
    let label: String
    let suffix: String?
    @Binding var value: Double
    var minValue: Double = 0
    var step: Double = 10
    var subtitle: String? = nil
    var sheetTitle: String = "Price"

    @State private var isPresenting = false

    var body: some View {
        Button(action: { isPresenting = true }) {
            HStack {
                Text(label)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                Spacer()
                HStack(spacing: 4) {
                    Text(Money(amount: value).formatted)
                        .font(.headline)
                        .foregroundColor(.primary)
                    if let suffix {
                        Text(suffix)
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .padding(.leading, 4)
                }
            }
            .padding(14)
            .background(Color(.systemGray6))
            .cornerRadius(10)
        }
        .buttonStyle(.plain)
        .sheet(isPresented: $isPresenting) {
            PriceEditorSheet(
                title: sheetTitle,
                value: $value,
                minValue: minValue,
                step: step,
                subtitle: subtitle
            )
        }
    }
}

#Preview {
    struct Harness: View {
        @State private var price: Double = 350
        var body: some View {
            VStack(spacing: 16) {
                PriceEditorRow(label: "Weekly rent price", suffix: "/ week", value: $price, minValue: 50)
                PriceEditorRow(label: "Sale price", suffix: nil, value: .constant(25000), minValue: 0)
            }
            .padding()
        }
    }
    return Harness()
}
