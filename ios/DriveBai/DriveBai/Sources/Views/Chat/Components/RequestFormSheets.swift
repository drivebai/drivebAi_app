import SwiftUI

// MARK: - Payment Request Form (Manual Payment / Additional Fee)

struct PaymentRequestForm: View {
    @Binding var input: CreateRequestInput

    @State private var amountText = ""

    var body: some View {
        TextField("Title (e.g. Weekly rent payment)", text: $input.title)

        HStack {
            Text(input.currency)
                .foregroundColor(.secondary)
            TextField("Amount", text: $amountText)
                .keyboardType(.decimalPad)
                .onChange(of: amountText) { _, newValue in
                    input.amount = Double(newValue)
                }
        }

        TextField("Description (optional)", text: $input.description, axis: .vertical)
            .lineLimit(2...4)
    }
}

// MARK: - Delayed Payment Request Form

struct DelayedPaymentRequestForm: View {
    @Binding var input: CreateRequestInput

    @State private var amountText = ""

    var body: some View {
        TextField("Title (e.g. Delayed rent - Week 3)", text: $input.title)

        HStack {
            Text(input.currency)
                .foregroundColor(.secondary)
            TextField("Amount (optional)", text: $amountText)
                .keyboardType(.decimalPad)
                .onChange(of: amountText) { _, newValue in
                    input.amount = Double(newValue)
                }
        }

        TextField("Reason for delay", text: $input.description, axis: .vertical)
            .lineLimit(2...4)
    }
}

// MARK: - Mechanic Service Request Form

struct MechanicServiceRequestForm: View {
    @Binding var input: CreateRequestInput

    @State private var amountText = ""

    var body: some View {
        TextField("Service title (e.g. Oil change needed)", text: $input.title)

        TextField("Describe the issue or service needed", text: $input.description, axis: .vertical)
            .lineLimit(3...6)

        HStack {
            Text(input.currency)
                .foregroundColor(.secondary)
            TextField("Estimated cost (optional)", text: $amountText)
                .keyboardType(.decimalPad)
                .onChange(of: amountText) { _, newValue in
                    input.amount = Double(newValue)
                }
        }
    }
}

// MARK: - Generic Request Form

struct GenericRequestForm: View {
    @Binding var input: CreateRequestInput

    var body: some View {
        TextField("Request title", text: $input.title)

        TextField("Describe your request in detail", text: $input.description, axis: .vertical)
            .lineLimit(3...6)
    }
}
