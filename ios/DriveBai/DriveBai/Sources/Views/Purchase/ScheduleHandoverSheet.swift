import SwiftUI

/// Modal used by the seller once payment is authorized.  Captures a
/// meetup time + place and hands it back to `ChatViewModel` for the
/// backend call.
struct ScheduleHandoverSheet: View {
    let purchaseRequest: PurchaseRequest
    let onSubmit: (Date, String) async -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var scheduledAt: Date = Date().addingTimeInterval(60 * 60 * 24)
    @State private var location: String = ""
    @State private var isSubmitting = false

    private var isValid: Bool {
        !location.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && scheduledAt > Date()
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("When") {
                    DatePicker(
                        "Meetup time",
                        selection: $scheduledAt,
                        in: Date()...,
                        displayedComponents: [.date, .hourAndMinute]
                    )
                }
                Section("Where") {
                    TextField("Address or landmark", text: $location, axis: .vertical)
                        .lineLimit(2...4)
                }
                Section {
                    Button {
                        Task {
                            isSubmitting = true
                            defer { isSubmitting = false }
                            await onSubmit(scheduledAt, location)
                            dismiss()
                        }
                    } label: {
                        HStack {
                            if isSubmitting { ProgressView() }
                            Text(isSubmitting ? "Sending…" : "Confirm meetup")
                                .font(.headline)
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .disabled(!isValid || isSubmitting)
                } footer: {
                    Text("Both parties get a chat notification. You can update the time by scheduling again.")
                }
            }
            .navigationTitle("Schedule handover")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSubmitting)
                }
            }
        }
    }
}
