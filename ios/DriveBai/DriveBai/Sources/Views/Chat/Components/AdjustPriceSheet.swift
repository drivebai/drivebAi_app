import SwiftUI

struct AdjustPriceSheet: View {
    let leaseRequest: LeaseRequest
    let onSave: (Double) -> Void
    @Environment(\.dismiss) private var dismiss

    @State private var priceText: String
    @State private var showValidationError = false

    private let step = 10.0

    init(leaseRequest: LeaseRequest, onSave: @escaping (Double) -> Void) {
        self.leaseRequest = leaseRequest
        self.onSave = onSave
        _priceText = State(initialValue: String(format: "%.0f", leaseRequest.effectiveWeeklyPrice))
    }

    private var parsedPrice: Double? {
        guard let value = Double(priceText), value >= 1 else { return nil }
        return value
    }

    private var previewTotal: String? {
        guard let price = parsedPrice else { return nil }
        let total = price * Double(leaseRequest.weeks)
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = leaseRequest.currency
        return formatter.string(from: NSNumber(value: total))
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                // Base price context
                HStack {
                    Text("Listing price")
                        .foregroundColor(.secondary)
                    Spacer()
                    Text("\(leaseRequest.formattedWeeklyPrice ?? "")/wk")
                        .fontWeight(.medium)
                }
                .padding(.horizontal)

                Divider()

                // Price input
                VStack(spacing: 12) {
                    Text("Set weekly price")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal)

                    HStack(spacing: 16) {
                        Button {
                            adjustPrice(by: -step)
                        } label: {
                            Image(systemName: "minus.circle.fill")
                                .font(.title2)
                                .foregroundColor(.driveBaiPrimary)
                        }

                        HStack(spacing: 4) {
                            Text(currencySymbol)
                                .font(.title2)
                                .foregroundColor(.secondary)
                            TextField("0", text: $priceText)
                                .keyboardType(.decimalPad)
                                .font(.title.weight(.semibold))
                                .multilineTextAlignment(.center)
                                .frame(minWidth: 80)
                        }

                        Button {
                            adjustPrice(by: step)
                        } label: {
                            Image(systemName: "plus.circle.fill")
                                .font(.title2)
                                .foregroundColor(.driveBaiPrimary)
                        }
                    }
                    .padding(.horizontal)

                    if showValidationError {
                        Text("Please enter a valid price (minimum $1)")
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                }

                Divider()

                // Total preview
                if let totalText = previewTotal {
                    HStack {
                        Text("New total (\(leaseRequest.weeks) \(leaseRequest.weeks == 1 ? "week" : "weeks"))")
                            .foregroundColor(.secondary)
                        Spacer()
                        Text(totalText)
                            .fontWeight(.semibold)
                    }
                    .padding(.horizontal)
                }

                Spacer()

                Button {
                    guard let price = parsedPrice else {
                        showValidationError = true
                        return
                    }
                    onSave(price)
                    dismiss()
                } label: {
                    Text("Save Price")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                }
                .padding(.horizontal)
                .padding(.bottom)
            }
            .padding(.top)
            .navigationTitle("Adjust Weekly Price")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private var currencySymbol: String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = leaseRequest.currency
        return formatter.currencySymbol ?? "$"
    }

    private func adjustPrice(by delta: Double) {
        let current = parsedPrice ?? leaseRequest.effectiveWeeklyPrice
        let new = max(1, current + delta)
        priceText = String(format: "%.0f", new)
        showValidationError = false
    }
}
