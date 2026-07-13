import SwiftUI
import CoreLocation

/// Modal used by the seller once payment is authorized.  Captures a
/// meetup time + place and hands it back to `ChatViewModel` for the
/// backend call.
///
/// The "Where" field is a real map/address selector (the same
/// `CarPickupLocationPickerView` the listing wizard uses) rather than
/// free text, so the handover carries actual coordinates the buyer can
/// route to. `onSubmit` therefore also carries lat/lng (the API accepts
/// them). A location is required before Confirm.
struct ScheduleHandoverSheet: View {
    let purchaseRequest: PurchaseRequest
    let onSubmit: (Date, String, Double?, Double?) async -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var scheduledAt: Date = Date().addingTimeInterval(60 * 60 * 24)
    @State private var location: String = ""
    @State private var latitude: Double?
    @State private var longitude: Double?
    @State private var showLocationPicker = false
    @State private var isSubmitting = false

    /// A valid selection is a resolved address string plus its coordinates.
    private var hasLocation: Bool {
        !location.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && latitude != nil
            && longitude != nil
    }

    private var isValid: Bool {
        hasLocation && scheduledAt > Date()
    }

    private var initialPickerCoordinate: CLLocationCoordinate2D? {
        guard let lat = latitude, let lng = longitude else { return nil }
        return CLLocationCoordinate2D(latitude: lat, longitude: lng)
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
                    Button {
                        showLocationPicker = true
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: "mappin.and.ellipse")
                                .foregroundColor(.driveBaiPrimary)
                            VStack(alignment: .leading, spacing: 2) {
                                if hasLocation {
                                    Text(location)
                                        .font(.subheadline.weight(.medium))
                                        .foregroundColor(.primary)
                                        .multilineTextAlignment(.leading)
                                } else {
                                    Text("Choose location on map")
                                        .font(.subheadline)
                                        .foregroundColor(.primary)
                                }
                            }
                            Spacer(minLength: 8)
                            Text(hasLocation ? "Change" : "Select")
                                .font(.caption.weight(.semibold))
                                .foregroundColor(.driveBaiPrimary)
                            Image(systemName: "chevron.right")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                    .buttonStyle(.plain)
                    .disabled(isSubmitting)
                }
                Section {
                    Button {
                        Task {
                            isSubmitting = true
                            defer { isSubmitting = false }
                            await onSubmit(scheduledAt, location, latitude, longitude)
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
            .sheet(isPresented: $showLocationPicker) {
                // Reuse the listing wizard's map/address picker. With no
                // initial coordinate it defaults the camera to the device
                // fix (LocationManager) and reverse-geocodes the pin via
                // LocationGeocoder — exactly the behaviour we want here.
                CarPickupLocationPickerView(
                    initialCoordinate: initialPickerCoordinate,
                    onSave: { coordinate, address in
                        latitude = coordinate.latitude
                        longitude = coordinate.longitude
                        location = address.isEmpty ? "Selected location" : address.displaySummary
                    }
                )
            }
        }
    }
}
