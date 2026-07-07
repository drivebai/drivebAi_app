import SwiftUI
import MapKit
import UIKit

/// Tappable pickup-location row rendered inside `KeyHandoverCard` and
/// `PickupCountdownView`. Shows the best-available label (coords → area →
/// car street/area → generic fallback). Tapping presents a chooser:
/// "Open in Apple Maps" / "Open in Google Maps" / "Cancel". Google Maps
/// uses the `comgooglemaps://` app scheme when installed and falls back to
/// the `https://www.google.com/maps/dir/` universal link otherwise, so the
/// Google option is never a dead tap.
///
/// **Failsafe:** never crashes when both coordinate and address are missing —
/// the row disables itself and shows "See chat for the pickup location".
struct HandoverLocationRow: View {
    let handover: KeyHandover
    /// Optional car snapshot for street-address fallback. Handover payload
    /// does not carry a full address today, so this may be nil.
    var car: Car? = nil

    @State private var showChooser = false

    var body: some View {
        Button(action: openDirections) {
            HStack(spacing: 10) {
                Image(systemName: "location.fill")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundColor(canOpenMaps ? .driveBaiPrimary : .secondary)
                    .frame(width: 26, height: 26)
                    .background((canOpenMaps ? Color.driveBaiPrimary : Color.secondary).opacity(0.12))
                    .clipShape(RoundedRectangle(cornerRadius: 7))

                VStack(alignment: .leading, spacing: 2) {
                    Text(primaryLine)
                        .font(.subheadline.weight(.medium))
                        .foregroundColor(.primary)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    if let secondary = secondaryLine {
                        Text(secondary)
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }

                Spacer(minLength: 8)

                if canOpenMaps {
                    Image(systemName: "arrow.up.right.square")
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundColor(.driveBaiPrimary)
                }
            }
            .padding(.vertical, 8)
            .padding(.horizontal, 10)
            .background(Color(.systemGray6))
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .contentShape(RoundedRectangle(cornerRadius: 10))
        }
        .buttonStyle(.plain)
        .disabled(!canOpenMaps)
        .confirmationDialog("Get directions", isPresented: $showChooser, titleVisibility: .visible) {
            Button("Open in Apple Maps") { openAppleMaps() }
            Button("Open in Google Maps") { openGoogleMaps() }
            Button("Cancel", role: .cancel) { }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(Text("Pickup location: \(primaryLine)"))
        .accessibilityHint(canOpenMaps ? Text("Get directions") : Text(""))
    }

    // MARK: - Fallback chain

    private var coordinate: CLLocationCoordinate2D? {
        guard let lat = handover.pickupLatitude,
              let lng = handover.pickupLongitude,
              !(lat == 0 && lng == 0) else { return nil }
        // Guard the range: MKMapItem crashes on NaN and behaves poorly with
        // values outside the WGS-84 range.
        guard lat.isFinite, lng.isFinite,
              (-90.0...90.0).contains(lat),
              (-180.0...180.0).contains(lng) else { return nil }
        return CLLocationCoordinate2D(latitude: lat, longitude: lng)
    }

    /// Order: pickupArea → car street/area line → car area → nil
    private var addressText: String? {
        if !handover.pickupArea.isEmpty { return handover.pickupArea }
        if let car {
            let line = car.location.displayAddressLine
            if !line.isEmpty { return line }
            let area = car.location.displayArea
            if area != "Unknown", !area.isEmpty { return area }
        }
        return nil
    }

    private var primaryLine: String {
        addressText ?? "See chat for the pickup location"
    }

    private var secondaryLine: String? {
        canOpenMaps ? "Get directions" : nil
    }

    private var canOpenMaps: Bool { coordinate != nil || addressText != nil }

    // MARK: - Launch

    private func openDirections() {
        showChooser = true
    }

    /// Destination string shared by every URL-based launch path:
    /// "lat,lng" when validated coordinates exist, else the address text.
    private var destinationText: String? {
        if let coord = coordinate { return "\(coord.latitude),\(coord.longitude)" }
        return addressText
    }

    private func openAppleMaps() {
        if let coord = coordinate {
            let placemark = MKPlacemark(coordinate: coord)
            let item = MKMapItem(placemark: placemark)
            item.name = "Pickup"
            item.openInMaps(launchOptions: [
                MKLaunchOptionsDirectionsModeKey: MKLaunchOptionsDirectionsModeDriving
            ])
        } else if let text = addressText {
            // URLComponents percent-encodes the query items, so addresses
            // containing spaces, commas, '&', '=', or unicode stay intact.
            var components = URLComponents()
            components.scheme = "https"
            components.host = "maps.apple.com"
            components.path = "/"
            components.queryItems = [
                URLQueryItem(name: "daddr", value: text),
                URLQueryItem(name: "dirflg", value: "d")
            ]
            if let url = components.url {
                UIApplication.shared.open(url)
            }
        }
    }

    private var googleMapsAvailable: Bool {
        guard let url = URL(string: "comgooglemaps://") else { return false }
        return UIApplication.shared.canOpenURL(url)
    }

    /// Google Maps app when installed, otherwise the supported
    /// `https://www.google.com/maps/dir/?api=1` universal link in Safari —
    /// the Google button is never a dead tap.
    private func openGoogleMaps() {
        guard let destination = destinationText else { return }

        var components = URLComponents()
        if googleMapsAvailable {
            components.scheme = "comgooglemaps"
            components.host = ""  // keeps the canonical "comgooglemaps://" form
            components.queryItems = [
                URLQueryItem(name: "daddr", value: destination),
                URLQueryItem(name: "directionsmode", value: "driving")
            ]
        } else {
            components.scheme = "https"
            components.host = "www.google.com"
            components.path = "/maps/dir/"
            components.queryItems = [
                URLQueryItem(name: "api", value: "1"),
                URLQueryItem(name: "destination", value: destination),
                URLQueryItem(name: "travelmode", value: "driving")
            ]
        }
        if let url = components.url {
            UIApplication.shared.open(url)
        }
    }
}
