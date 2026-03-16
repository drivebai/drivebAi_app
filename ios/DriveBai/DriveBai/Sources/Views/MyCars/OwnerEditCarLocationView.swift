import SwiftUI
import MapKit
import os

private let logger = Logger(subsystem: "com.drivebai", category: "OwnerEditCarLocation")

// MARK: - Owner Edit Car Location Flow (3-step)

struct OwnerEditCarLocationView: View {
    let car: Car
    @Environment(\.dismiss) private var dismiss
    @StateObject private var store = OwnerCarsStore.shared

    @State private var step: EditLocationStep = .selectOnMap
    @State private var selectedCoordinate: CLLocationCoordinate2D?
    @State private var area: String = ""
    @State private var street: String = ""
    @State private var block: String = ""
    @State private var zip: String = ""
    @State private var isSaving = false
    @State private var errorMessage: String?

    enum EditLocationStep {
        case selectOnMap
        case confirmLocation
        case editDetails
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                // Header
                headerBar

                // Content based on step
                switch step {
                case .selectOnMap:
                    SelectLocationMapStep(
                        car: car,
                        selectedCoordinate: $selectedCoordinate
                    )
                case .confirmLocation:
                    ConfirmLocationStep(
                        selectedCoordinate: selectedCoordinate
                    )
                case .editDetails:
                    EditLocationDetailsStep(
                        area: $area,
                        street: $street,
                        block: $block,
                        zip: $zip
                    )
                }

                // Error banner
                if let error = errorMessage {
                    Text(error)
                        .font(.caption)
                        .foregroundColor(.red)
                        .padding(.horizontal, 16)
                        .padding(.top, 8)
                }

                // CTA button
                ctaButton
            }
            .background(Color(.systemBackground))
            .navigationBarHidden(true)
            .onAppear {
                prefillFromCar()
            }
        }
    }

    // MARK: - Header

    private var headerBar: some View {
        HStack {
            Button(action: handleBack) {
                Image(systemName: "chevron.left")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundColor(.primary)
            }

            Spacer()

            Text("Reset car location")
                .font(.headline)

            Spacer()

            // Spacer for balance
            Color.clear.frame(width: 24, height: 24)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
    }

    // MARK: - CTA Button

    private var ctaButton: some View {
        Button(action: handleCTA) {
            Group {
                if isSaving {
                    ProgressView()
                        .tint(.white)
                } else {
                    Text(ctaTitle)
                }
            }
            .font(.headline)
            .foregroundColor(.white)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(ctaEnabled ? Color.driveBaiPrimary : Color.gray)
            .cornerRadius(12)
        }
        .disabled(!ctaEnabled || isSaving)
        .padding(16)
    }

    private var ctaTitle: String {
        switch step {
        case .selectOnMap: return "Check details"
        case .confirmLocation: return "Check details"
        case .editDetails: return "Save"
        }
    }

    private var ctaEnabled: Bool {
        switch step {
        case .selectOnMap:
            return selectedCoordinate != nil
        case .confirmLocation:
            return selectedCoordinate != nil
        case .editDetails:
            return selectedCoordinate != nil
        }
    }

    // MARK: - Actions

    private func handleBack() {
        switch step {
        case .selectOnMap:
            dismiss()
        case .confirmLocation:
            withAnimation { step = .selectOnMap }
        case .editDetails:
            withAnimation { step = .confirmLocation }
        }
    }

    private func handleCTA() {
        switch step {
        case .selectOnMap:
            withAnimation { step = .confirmLocation }
        case .confirmLocation:
            withAnimation { step = .editDetails }
        case .editDetails:
            saveLocation()
        }
    }

    private func prefillFromCar() {
        if car.location.hasCoordinate {
            selectedCoordinate = CLLocationCoordinate2D(
                latitude: car.location.latitude,
                longitude: car.location.longitude
            )
        }
        area = car.location.area.isEmpty ? car.location.neighborhood : car.location.area
        street = car.location.street.isEmpty ? car.location.address : car.location.street
        block = car.location.block
        zip = car.location.zip
    }

    private func saveLocation() {
        guard let coord = selectedCoordinate else { return }
        isSaving = true
        errorMessage = nil

        logger.info("Saving location: lat=\(coord.latitude) lng=\(coord.longitude) area=\(area) street=\(street)")

        Task {
            let request = UpdateCarLocationRequest(
                latitude: coord.latitude,
                longitude: coord.longitude,
                area: area.isEmpty ? nil : area,
                street: street.isEmpty ? nil : street,
                block: block.isEmpty ? nil : block,
                zip: zip.isEmpty ? nil : zip
            )

            if let _ = await store.updateCarLocation(carId: car.id, request: request) {
                logger.info("Location saved successfully for car \(car.id)")
                dismiss()
            } else {
                errorMessage = store.error ?? "Failed to save location"
                logger.error("Failed to save location: \(store.error ?? "unknown")")
            }
            isSaving = false
        }
    }
}

// MARK: - Step 1: Select Location on Map

struct SelectLocationMapStep: View {
    let car: Car
    @Binding var selectedCoordinate: CLLocationCoordinate2D?
    @State private var cameraPosition: MapCameraPosition = .automatic

    var body: some View {
        ZStack(alignment: .bottom) {
            MapReader { proxy in
                Map(position: $cameraPosition) {
                    if let coord = selectedCoordinate {
                        Annotation("Car location", coordinate: coord) {
                            CarMapPin(isSelected: true)
                        }
                    }
                }
                .mapStyle(.standard)
                .mapControls {
                    MapCompass()
                    MapScaleView()
                }
                .onTapGesture { position in
                    if let coord = proxy.convert(position, from: .local) {
                        withAnimation(.easeInOut(duration: 0.2)) {
                            selectedCoordinate = coord
                        }
                        #if DEBUG
                        print("[SelectLocationMap] Pin placed at \(coord.latitude), \(coord.longitude)")
                        #endif
                    }
                }
            }

            // Instruction label
            Text("Select location on map or enter address")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .padding(.horizontal, 16)
                .padding(.vertical, 10)
                .background(Color(.systemBackground).opacity(0.9))
                .cornerRadius(8)
                .shadow(color: .black.opacity(0.1), radius: 4, y: 2)
                .padding(.bottom, 16)
        }
        .onAppear {
            setInitialCamera()
        }
    }

    private func setInitialCamera() {
        if let coord = selectedCoordinate {
            cameraPosition = .region(MKCoordinateRegion(
                center: coord,
                span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
            ))
        } else if car.location.hasCoordinate {
            let coord = CLLocationCoordinate2D(latitude: car.location.latitude, longitude: car.location.longitude)
            selectedCoordinate = coord
            cameraPosition = .region(MKCoordinateRegion(
                center: coord,
                span: MKCoordinateSpan(latitudeDelta: 0.01, longitudeDelta: 0.01)
            ))
        }
        // If no coordinate, let the map default (user's location or world view)
    }
}

// MARK: - Step 2: Confirm Location

struct ConfirmLocationStep: View {
    let selectedCoordinate: CLLocationCoordinate2D?

    var body: some View {
        VStack(spacing: 0) {
            // Map with pin (non-interactive)
            if let coord = selectedCoordinate {
                Map(initialPosition: .region(MKCoordinateRegion(
                    center: coord,
                    span: MKCoordinateSpan(latitudeDelta: 0.008, longitudeDelta: 0.008)
                ))) {
                    Annotation("Car location", coordinate: coord) {
                        CarMapPin(isSelected: true)
                    }
                }
                .mapStyle(.standard)
                .allowsHitTesting(false)
            }

            // Address confirmation text
            VStack(spacing: 8) {
                Text("(precise address as identified on map)")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
            }
            .padding(16)
            .frame(maxWidth: .infinity)
            .background(Color(.systemBackground))
        }
    }
}

// MARK: - Step 3: Edit Details Form

struct EditLocationDetailsStep: View {
    @Binding var area: String
    @Binding var street: String
    @Binding var block: String
    @Binding var zip: String

    var body: some View {
        ScrollView {
            VStack(spacing: 16) {
                HStack(spacing: 12) {
                    LocationField(title: "Area", text: $area)
                    LocationField(title: "Street", text: $street)
                }

                HStack(spacing: 12) {
                    LocationField(title: "Block", text: $block)
                    LocationField(title: "ZIP", text: $zip)
                }
            }
            .padding(16)
        }
    }
}

// MARK: - Location Field

private struct LocationField: View {
    let title: String
    @Binding var text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(.caption)
                .foregroundColor(.secondary)

            TextField(title, text: $text)
                .font(.body)
                .padding(12)
                .background(Color(.systemGray6))
                .cornerRadius(10)
                .overlay(
                    RoundedRectangle(cornerRadius: 10)
                        .stroke(Color(.systemGray4), lineWidth: 1)
                )
        }
    }
}
