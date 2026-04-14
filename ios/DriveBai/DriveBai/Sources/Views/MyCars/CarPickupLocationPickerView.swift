import SwiftUI
import MapKit
import CoreLocation

// MARK: - Resolved Address

struct ResolvedAddress: Equatable {
    var area: String
    var street: String
    var block: String
    var zip: String
    var addressLine: String

    static let empty = ResolvedAddress(area: "", street: "", block: "", zip: "", addressLine: "")

    var isEmpty: Bool {
        area.isEmpty && street.isEmpty && addressLine.isEmpty
    }

    var displaySummary: String {
        if !addressLine.isEmpty { return addressLine }
        var parts: [String] = []
        if !area.isEmpty { parts.append(area) }
        if !street.isEmpty { parts.append(street) }
        return parts.isEmpty ? "Unknown location" : parts.joined(separator: ", ")
    }
}

// MARK: - Picker View

struct CarPickupLocationPickerView: View {
    /// Initial coordinate to center on (if user already picked before)
    var initialCoordinate: CLLocationCoordinate2D?
    /// Called when user confirms location
    var onSave: (CLLocationCoordinate2D, ResolvedAddress) -> Void

    @Environment(\.dismiss) private var dismiss
    @StateObject private var locationManager = LocationManager()

    @State private var cameraPosition: MapCameraPosition = .automatic
    @State private var selectedCoordinate: CLLocationCoordinate2D?
    @State private var resolvedAddress: ResolvedAddress = .empty
    @State private var isGeocoding = false
    @State private var geocodeError: String?
    @State private var geocodeTask: Task<Void, Never>?
    @State private var hasSetInitialPosition = false

    // Tracks the coordinate of the most recent successful reverse-geocode so we
    // can skip re-geocoding when the camera settles within a small distance of
    // the previous result.
    @State private var lastGeocodedCoordinate: CLLocationCoordinate2D?

    // Monotonically increasing request ID so only the latest reverse-geocode
    // result can mutate UI state (prevents stale async responses from flicker).
    @State private var geocodeRequestID: Int = 0

    // Minimum movement (meters) before we bother running another geocode.
    private let geocodeSkipDistanceMeters: Double = 15

    // Default fallback: Kuwait City center
    private let defaultCoordinate = CLLocationCoordinate2D(latitude: 29.3759, longitude: 47.9774)

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                // Map with center pin
                mapSection

                // Address details
                addressSection

                // Save button
                saveButton
            }
            .background(Color(.systemBackground))
            .navigationTitle("Pickup Location")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
            .onAppear {
                setupInitialPosition()
            }
        }
    }

    // MARK: - Map Section

    private var mapSection: some View {
        ZStack {
            Map(position: $cameraPosition) {
            }
            .mapStyle(.standard)
            .mapControls {
                MapCompass()
                MapScaleView()
            }
            .onMapCameraChange(frequency: .onEnd) { context in
                let center = context.camera.centerCoordinate
                selectedCoordinate = center
                debouncedGeocode(coordinate: center)
            }

            // Center pin overlay
            VStack {
                Image(systemName: "mappin")
                    .font(.system(size: 36))
                    .foregroundColor(Color.driveBaiPrimary)
                    .shadow(color: .black.opacity(0.3), radius: 2, y: 2)

                // Pin shadow dot
                Circle()
                    .fill(Color.black.opacity(0.2))
                    .frame(width: 8, height: 4)
            }

            // My Location button
            VStack {
                Spacer()
                HStack {
                    Spacer()
                    Button(action: centerOnUserLocation) {
                        Image(systemName: "location.fill")
                            .font(.system(size: 16))
                            .foregroundColor(Color.driveBaiPrimary)
                            .padding(10)
                            .background(Color(.systemBackground))
                            .clipShape(Circle())
                            .shadow(color: .black.opacity(0.15), radius: 4, y: 2)
                    }
                    .padding(.trailing, 16)
                    .padding(.bottom, 16)
                }
            }
        }
    }

    // MARK: - Address Section

    private var addressSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "mappin.and.ellipse")
                    .foregroundColor(Color.driveBaiPrimary)

                // Show the last resolved address whenever we have one — even while
                // a new geocode is in flight. This prevents the text from
                // blinking between the address and "Finding address..." on every
                // camera settle.
                if !resolvedAddress.isEmpty {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(resolvedAddress.displaySummary)
                            .font(.subheadline)
                            .fontWeight(.medium)

                        if !resolvedAddress.zip.isEmpty {
                            Text("ZIP: \(resolvedAddress.zip)")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                } else if isGeocoding {
                    // No known address yet — show the "looking up" state.
                    HStack(spacing: 8) {
                        ProgressView()
                            .scaleEffect(0.8)
                        Text("Finding address...")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                } else if let error = geocodeError {
                    Text(error)
                        .font(.subheadline)
                        .foregroundColor(.orange)
                } else {
                    Text("Move the map to select a location")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                }

                Spacer()

                // Subtle inline spinner when refreshing an already-resolved address.
                if isGeocoding && !resolvedAddress.isEmpty {
                    ProgressView()
                        .scaleEffect(0.8)
                }
            }
            .padding(16)
            .background(Color(.systemGray6))
            .cornerRadius(12)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
    }

    // MARK: - Save Button

    private var saveButton: some View {
        Button(action: save) {
            Text("Save Location")
                .font(.headline)
                .foregroundColor(.white)
                .frame(maxWidth: .infinity)
                .padding(16)
                .background(canSave ? Color.driveBaiPrimary : Color.gray)
                .cornerRadius(12)
        }
        .disabled(!canSave)
        .padding(.horizontal, 16)
        .padding(.bottom, 16)
    }

    private var canSave: Bool {
        // Allow saving as long as we have a coordinate and a resolved address.
        // The in-flight spinner for a background refresh should not block save.
        guard selectedCoordinate != nil else { return false }
        if !resolvedAddress.isEmpty { return true }
        return !isGeocoding
    }

    // MARK: - Actions

    private func setupInitialPosition() {
        guard !hasSetInitialPosition else { return }
        hasSetInitialPosition = true

        if let initial = initialCoordinate, initial.latitude != 0, initial.longitude != 0 {
            // Use previously selected coordinate. The programmatic camera move
            // below will trigger onMapCameraChange → debouncedGeocode, so we do
            // NOT also call reverseGeocode here (that caused two races to
            // flicker the "Finding address..." label).
            selectedCoordinate = initial
            cameraPosition = .region(MKCoordinateRegion(
                center: initial,
                span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
            ))
        } else {
            // Request user location and center on it
            locationManager.requestPermission()
            // Wait briefly for location to arrive, then set position
            Task {
                try? await Task.sleep(for: .milliseconds(800))
                await MainActor.run {
                    if let userLoc = locationManager.lastLocation {
                        // The programmatic camera move triggers onMapCameraChange,
                        // which schedules a single debounced geocode. No direct
                        // call here — avoids racing with the camera-change path.
                        selectedCoordinate = userLoc
                        cameraPosition = .region(MKCoordinateRegion(
                            center: userLoc,
                            span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
                        ))
                    } else {
                        // Fallback to default
                        selectedCoordinate = defaultCoordinate
                        cameraPosition = .region(MKCoordinateRegion(
                            center: defaultCoordinate,
                            span: MKCoordinateSpan(latitudeDelta: 0.05, longitudeDelta: 0.05)
                        ))
                    }
                }
            }
        }
    }

    private func centerOnUserLocation() {
        locationManager.requestPermission()
        locationManager.requestLocation()

        Task {
            try? await Task.sleep(for: .milliseconds(600))
            await MainActor.run {
                if let userLoc = locationManager.lastLocation {
                    withAnimation {
                        cameraPosition = .region(MKCoordinateRegion(
                            center: userLoc,
                            span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
                        ))
                    }
                }
            }
        }
    }

    private func debouncedGeocode(coordinate: CLLocationCoordinate2D) {
        geocodeTask?.cancel()
        geocodeTask = Task {
            try? await Task.sleep(for: .milliseconds(500))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                reverseGeocode(coordinate: coordinate)
            }
        }
    }

    private func reverseGeocode(coordinate: CLLocationCoordinate2D) {
        // Skip if we already resolved an address for a coordinate very close to
        // this one. Prevents redundant re-geocodes (and the flicker they cause)
        // when the camera settles slightly after a programmatic move.
        if let last = lastGeocodedCoordinate, !resolvedAddress.isEmpty {
            let a = CLLocation(latitude: last.latitude, longitude: last.longitude)
            let b = CLLocation(latitude: coordinate.latitude, longitude: coordinate.longitude)
            if a.distance(from: b) < geocodeSkipDistanceMeters {
                return
            }
        }

        geocodeRequestID += 1
        let requestID = geocodeRequestID

        isGeocoding = true
        geocodeError = nil

        let location = CLLocation(latitude: coordinate.latitude, longitude: coordinate.longitude)
        let geocoder = CLGeocoder()

        Task {
            do {
                let placemarks = try await geocoder.reverseGeocodeLocation(location)
                await MainActor.run {
                    // Drop stale responses so a slow geocode from a previous
                    // coordinate can't overwrite a newer one.
                    guard requestID == geocodeRequestID else { return }

                    if let placemark = placemarks.first {
                        let area = placemark.subLocality ?? placemark.locality ?? placemark.administrativeArea ?? ""
                        let thoroughfare = placemark.thoroughfare ?? ""
                        let subThoroughfare = placemark.subThoroughfare ?? ""
                        let street = [thoroughfare, subThoroughfare].filter { !$0.isEmpty }.joined(separator: " ")
                        let zip = placemark.postalCode ?? ""

                        // Build a human-readable address line
                        var parts: [String] = []
                        if !street.isEmpty { parts.append(street) }
                        if !area.isEmpty { parts.append(area) }
                        if !zip.isEmpty { parts.append(zip) }
                        let addressLine = parts.joined(separator: ", ")

                        resolvedAddress = ResolvedAddress(
                            area: area,
                            street: street,
                            block: "",
                            zip: zip,
                            addressLine: addressLine
                        )
                        lastGeocodedCoordinate = coordinate
                    } else if resolvedAddress.isEmpty {
                        geocodeError = "Could not resolve address"
                    }
                    isGeocoding = false
                }
            } catch {
                await MainActor.run {
                    guard requestID == geocodeRequestID else { return }
                    // Keep any previously resolved address visible on failure.
                    if resolvedAddress.isEmpty {
                        geocodeError = "Address lookup failed. Try again."
                    }
                    isGeocoding = false
                    #if DEBUG
                    print("[CarPickupLocationPicker] Geocode error: \(error.localizedDescription)")
                    #endif
                }
            }
        }
    }

    private func save() {
        guard let coord = selectedCoordinate else { return }
        onSave(coord, resolvedAddress)
        dismiss()
    }
}
