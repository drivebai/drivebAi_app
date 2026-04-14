import SwiftUI

// MARK: - Price Editor Mode

enum PriceEditorMode: String, CaseIterable, Identifiable {
    case adjust = "Adjust"
    case set = "Set"
    case dynamic = "Dynamic"

    var id: String { rawValue }
}

// MARK: - Price Editor Sheet

/// A reusable bottom-sheet price editor with Adjust / Set / Dynamic segments.
/// - Adjust: large centered price with -/+ buttons (±$10 per tap)
/// - Set: numeric text entry with a Done toolbar button
/// - Dynamic: placeholder ("coming soon")
///
/// Bind to a `Double` (the underlying price amount). The sheet preserves the
/// value when switching modes. Enforces `minValue` (default 0).
struct PriceEditorSheet: View {
    let title: String
    @Binding var value: Double
    var minValue: Double = 0
    var step: Double = 10
    var subtitle: String? = nil

    @Environment(\.dismiss) private var dismiss
    @State private var mode: PriceEditorMode = .adjust
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

            // Segmented picker
            Picker("Mode", selection: $mode) {
                ForEach(PriceEditorMode.allCases) { m in
                    Text(m.rawValue).tag(m)
                }
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 20)
            .padding(.bottom, 28)
            .onChange(of: mode) { _, newMode in
                if newMode != .set {
                    isTextFieldFocused = false
                }
                if newMode == .set {
                    textValue = String(Int(value))
                }
            }

            // Mode-specific content
            Group {
                switch mode {
                case .adjust:
                    adjustView
                case .set:
                    setView
                case .dynamic:
                    dynamicView
                }
            }
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
            textValue = String(Int(max(value, minValue)))
        }
    }

    // MARK: - Adjust mode

    private var adjustView: some View {
        VStack(spacing: 20) {
            Spacer(minLength: 0)

            // Big centered price
            VStack(spacing: 6) {
                Text(formattedValue)
                    .font(.system(size: 56, weight: .bold))
                    .foregroundColor(.primary)
                    .contentTransition(.numericText())
                    .animation(.easeOut(duration: 0.15), value: value)

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

    // MARK: - Set mode

    private var setView: some View {
        VStack(spacing: 20) {
            Spacer(minLength: 0)

            HStack(spacing: 4) {
                Text("$")
                    .font(.system(size: 44, weight: .bold))
                    .foregroundColor(.secondary)
                TextField("0", text: $textValue)
                    .font(.system(size: 56, weight: .bold))
                    .keyboardType(.numberPad)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: true, vertical: false)
                    .focused($isTextFieldFocused)
                    .onChange(of: textValue) { _, newText in
                        let filtered = newText.filter { $0.isNumber }
                        if filtered != newText { textValue = filtered }
                        if let parsed = Double(filtered) {
                            value = max(parsed, minValue)
                        } else if filtered.isEmpty {
                            value = minValue
                        }
                    }
            }

            if let subtitle {
                Text(subtitle)
                    .font(.footnote)
                    .foregroundColor(.orange)
            }

            Spacer(minLength: 0)
        }
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button("Done") { isTextFieldFocused = false }
                    .fontWeight(.semibold)
            }
        }
        .onAppear {
            textValue = String(Int(max(value, minValue)))
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
                isTextFieldFocused = true
            }
        }
    }

    // MARK: - Dynamic mode

    private var dynamicView: some View {
        VStack(spacing: 16) {
            Spacer(minLength: 0)
            Image(systemName: "sparkles")
                .font(.system(size: 40))
                .foregroundColor(.driveBaiPrimary)
            Text("Dynamic pricing")
                .font(.title3)
                .fontWeight(.semibold)
            Text("Smart pricing that adjusts to demand is coming soon.")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 24)
            Spacer(minLength: 0)
        }
    }

    // MARK: - Helpers

    private var formattedValue: String {
        Money(amount: value).formatted
    }

    private func increment() {
        value += step
    }

    private func decrement() {
        value = max(minValue, value - step)
    }

    private func commitAndDismiss() {
        isTextFieldFocused = false
        // Ensure text edits are flushed into value before closing
        if mode == .set, let parsed = Double(textValue) {
            value = max(parsed, minValue)
        }
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
                PriceEditorRow(label: "Sale price", suffix: nil, value: .constant(25000), minValue: 1000)
            }
            .padding()
        }
    }
    return Harness()
}
