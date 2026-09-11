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

    /// The server refuses a handover later than hold end − 48h window − 24h
    /// margin (HANDOVER_TOO_LATE). Keep the picker inside that so the seller
    /// never schedules something the buyer's payment cannot cover.
    private var latestHandover: Date? { purchaseRequest.latestHandoverAt }

    private var pickerRange: ClosedRange<Date> {
        if let latest = latestHandover, latest > Date() {
            return Date()...latest
        }
        return Date()...Date.distantFuture
    }

    private var holdTooShort: Bool {
        if let latest = latestHandover { return latest <= Date() }
        return false
    }

    private var isValid: Bool {
        hasLocation && scheduledAt > Date() && !holdTooShort
            && (latestHandover.map { scheduledAt <= $0 } ?? true)
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
                        in: pickerRange,
                        displayedComponents: [.date, .hourAndMinute]
                    )
                    if let latest = latestHandover {
                        if holdTooShort {
                            Label("The buyer's payment hold ends too soon for the 48-hour inspection window. Don't hand over the keys under this payment — once the hold lapses the buyer can make a new offer.",
                                  systemImage: "exclamationmark.triangle.fill")
                                .font(.caption)
                                .foregroundColor(.red)
                        } else {
                            Label("Hand over by \(latest.formatted(date: .abbreviated, time: .shortened)) — the buyer's 48-hour inspection window has to close before their payment hold ends.",
                                  systemImage: "clock.badge.exclamationmark")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
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
