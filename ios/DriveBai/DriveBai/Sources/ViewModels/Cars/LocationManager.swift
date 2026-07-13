import Foundation
import CoreLocation

// MARK: - Resolved Address
//
// Structured reverse-geocode result shared across the pickup-location picker,
// the create-listing wizard (defaults the address from a device fix) and the
// BoS/handover flows. Lives here (next to the geocode helper) rather than in a
// single View so any wave can consume it.

struct ResolvedAddress: Equatable {
    var area: String
    var street: String
    var block: String
    var zip: String
    var addressLine: String
    /// Neighbourhood / sub-locality when the geocoder provides one.
    var neighborhood: String

    static let empty = ResolvedAddress(
        area: "", street: "", block: "", zip: "", addressLine: "", neighborhood: ""
    )

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

extension ResolvedAddress {
    /// Builds structured address parts from a CLPlacemark. Extracted verbatim
    /// from CarPickupLocationPickerView so every caller resolves addresses the
    /// same way.
    init(placemark: CLPlacemark) {
        let neighborhood = placemark.subLocality ?? ""
        let area = placemark.subLocality ?? placemark.locality ?? placemark.administrativeArea ?? ""
        let thoroughfare = placemark.thoroughfare ?? ""
        let subThoroughfare = placemark.subThoroughfare ?? ""
        let street = [thoroughfare, subThoroughfare].filter { !$0.isEmpty }.joined(separator: " ")
        let zip = placemark.postalCode ?? ""

        // Human-readable single-line address.
        var parts: [String] = []
        if !street.isEmpty { parts.append(street) }
        if !area.isEmpty { parts.append(area) }
        if !zip.isEmpty { parts.append(zip) }
        let addressLine = parts.joined(separator: ", ")

        self.init(
            area: area,
            street: street,
            block: "",
            zip: zip,
            addressLine: addressLine,
            neighborhood: neighborhood
        )
    }
}

@MainActor
final class LocationManager: NSObject, ObservableObject {
    @Published var lastLocation: CLLocationCoordinate2D?
    @Published var authorizationStatus: CLAuthorizationStatus = .notDetermined

    private let manager = CLLocationManager()

    override init() {
        super.init()
        manager.delegate = self
        manager.desiredAccuracy = kCLLocationAccuracyHundredMeters
        authorizationStatus = manager.authorizationStatus
    }

    func requestPermission() {
        manager.requestWhenInUseAuthorization()
    }

    func requestLocation() {
        guard authorizationStatus == .authorizedWhenInUse || authorizationStatus == .authorizedAlways else {
            return
        }
        manager.requestLocation()
    }
}

extension LocationManager: CLLocationManagerDelegate {
    nonisolated func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        guard let location = locations.last else { return }
        Task { @MainActor in
            self.lastLocation = location.coordinate
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {
        #if DEBUG
        print("[LocationManager] Location error: \(error.localizedDescription)")
        #endif
    }

    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        let status = manager.authorizationStatus
        Task { @MainActor in
            self.authorizationStatus = status
            if status == .authorizedWhenInUse || status == .authorizedAlways {
                manager.requestLocation()
            }
        }
    }
}

// MARK: - Reverse geocode helper
//
// Shared async reverse-geocode used by the pickup-location picker, the
// create-listing wizard (W2-A) and (later) BoS/handover to default an address
// from a device fix. Extracted from CarPickupLocationPickerView so there is a
// single implementation. Swallows failures and returns nil (no address) so
// callers can decide how to surface it.

enum LocationGeocoder {
    /// Reverse-geocodes a coordinate into structured address parts.
    /// Returns nil when no placemark is found or the lookup fails.
    static func reverseGeocode(_ coordinate: CLLocationCoordinate2D) async -> ResolvedAddress? {
        let location = CLLocation(latitude: coordinate.latitude, longitude: coordinate.longitude)
        guard let placemark = try? await CLGeocoder().reverseGeocodeLocation(location).first else {
            return nil
        }
        return ResolvedAddress(placemark: placemark)
    }

    /// Convenience for callers that hold raw lat/lng (e.g. BoS/handover
    /// address defaults sourced from the API's *_lat / *_lng floats).
    static func reverseGeocode(latitude: Double, longitude: Double) async -> ResolvedAddress? {
        await reverseGeocode(CLLocationCoordinate2D(latitude: latitude, longitude: longitude))
    }
}
