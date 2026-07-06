import SwiftUI
import AVFoundation
import PhotosUI
import UIKit

// MARK: - Guided Photo Capture (QA pt 4, spec Section 5)
//
// Full-screen AVCaptureSession flow that walks the owner through the 8
// vehicle shots in `PhotoSlotType.sortOrder` order:
//
//   front → front-left ¾ → driver side → rear → rear-right ¾ →
//   passenger side → dashboard → interior
//
// Per slot: a translucent SF-Symbol silhouette overlay, a caption
// ("Shot 3 of 8 — Driver side") with a one-line hint, and progress dots.
// Shutter → freeze-frame review with Retake / Use Photo; Use Photo
// advances. Per-slot escape hatches: "Skip" (leaves the slot empty) and a
// per-slot Photos-library fallback. Close (top-left) exits early.
//
// Output: `[PhotoSlotType: Data]` (JPEG) with every shot captured so far —
// partial completion is allowed; photos remain optional at the wizard
// level. The caller writes each entry into the matching
// `CarPhotoSlot.localImageData`; the upload pipeline is unchanged.
//
// On devices without a camera (simulator) or when permission is denied,
// the flow degrades to a per-slot library picker so it stays usable.
//
// Foundation-owned shell — wave 2 wires it into the wizard and (stretch)
// the photos edit screen. Present with `.fullScreenCover`.

struct GuidedPhotoCaptureView: View {
    /// Slots to walk through, in order. Defaults to all 8 guided slots.
    var slots: [PhotoSlotType] = PhotoSlotType.allCases.sorted { $0.sortOrder < $1.sortOrder }
    /// Pre-existing shots (e.g. re-entering the flow) — shown as done.
    var initialCaptures: [PhotoSlotType: Data] = [:]
    /// Called exactly once, with everything captured, when the user finishes
    /// or closes early.
    let onComplete: ([PhotoSlotType: Data]) -> Void

    @Environment(\.dismiss) private var dismiss
    @StateObject private var camera = GuidedCameraController()

    @State private var currentIndex = 0
    @State private var captured: [PhotoSlotType: Data] = [:]
    @State private var reviewData: Data?
    @State private var showLibraryPicker = false
    @State private var libraryItem: PhotosPickerItem?
    @State private var didComplete = false

    private var currentSlot: PhotoSlotType? {
        slots.indices.contains(currentIndex) ? slots[currentIndex] : nil
    }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()

            if let reviewData, let image = UIImage(data: reviewData) {
                reviewScreen(image: image)
            } else if camera.status == .running {
                captureScreen
            } else {
                fallbackScreen
            }
        }
        .statusBarHidden()
        .onAppear {
            captured = initialCaptures
            camera.start()
        }
        .onDisappear {
            camera.stop()
        }
        .photosPicker(isPresented: $showLibraryPicker, selection: $libraryItem, matching: .images)
        .onChange(of: libraryItem) { _, newItem in
            guard let item = newItem else { return }
            libraryItem = nil
            Task {
                guard let data = try? await item.loadTransferable(type: Data.self),
                      let normalized = Self.normalizedJPEG(from: data) else { return }
                await MainActor.run { acceptShot(normalized) }
            }
        }
    }

    // MARK: - Live capture screen

    private var captureScreen: some View {
        ZStack {
            CameraPreviewView(session: camera.session)
                .ignoresSafeArea()

            // Silhouette overlay for the current slot.
            if let slot = currentSlot {
                Image(systemName: overlaySymbol(for: slot))
                    .resizable()
                    .scaledToFit()
                    .frame(maxWidth: 280, maxHeight: 200)
                    .foregroundColor(.white.opacity(0.35))
                    .scaleEffect(x: slot == .right ? -1 : 1, y: 1) // mirror for passenger side
                    .allowsHitTesting(false)
            }

            VStack {
                header
                Spacer()
                captionBlock
                controlsBlock
            }
        }
    }

    private var header: some View {
        HStack {
            Button {
                finish()
            } label: {
                Image(systemName: "xmark")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundColor(.white)
                    .frame(width: 36, height: 36)
                    .background(Color.black.opacity(0.5))
                    .clipShape(Circle())
            }
            .accessibilityLabel("Close")

            Spacer()

            Text("Tip: hold your phone in landscape for exterior shots")
                .font(.caption2)
                .foregroundColor(.white.opacity(0.7))
                .lineLimit(2)
                .multilineTextAlignment(.trailing)
        }
        .padding(.horizontal, 16)
        .padding(.top, 8)
    }

    private var captionBlock: some View {
        VStack(spacing: 6) {
            if let slot = currentSlot {
                Text("Shot \(currentIndex + 1) of \(slots.count) — \(slot.displayLabel)")
                    .font(.headline)
                    .foregroundColor(.white)
                Text(slot.guidedHint)
                    .font(.caption)
                    .foregroundColor(.white.opacity(0.8))
            }

            progressDots
            thumbnailStrip
        }
        .padding(.bottom, 12)
    }

    private var progressDots: some View {
        HStack(spacing: 6) {
            ForEach(Array(slots.enumerated()), id: \.element) { index, slot in
                Circle()
                    .fill(dotColor(index: index, slot: slot))
                    .frame(width: 8, height: 8)
            }
        }
        .padding(.top, 4)
    }

    private func dotColor(index: Int, slot: PhotoSlotType) -> Color {
        if captured[slot] != nil { return .green }
        if index == currentIndex { return .white }
        return .white.opacity(0.3)
    }

    @ViewBuilder
    private var thumbnailStrip: some View {
        let done = slots.filter { captured[$0] != nil }
        if !done.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(done, id: \.self) { slot in
                        if let data = captured[slot], let image = UIImage(data: data) {
                            Image(uiImage: image)
                                .resizable()
                                .scaledToFill()
                                .frame(width: 44, height: 44)
                                .clipShape(RoundedRectangle(cornerRadius: 6))
                                .overlay(alignment: .bottomTrailing) {
                                    Image(systemName: "checkmark.circle.fill")
                                        .font(.system(size: 12))
                                        .foregroundColor(.green)
                                        .background(Circle().fill(.white).frame(width: 10, height: 10))
                                        .offset(x: 3, y: 3)
                                }
                        }
                    }
                }
                .padding(.horizontal, 16)
            }
            .frame(height: 50)
        }
    }

    private var controlsBlock: some View {
        VStack(spacing: 14) {
            // Shutter
            Button {
                camera.capture { data in
                    guard let data else { return }
                    reviewData = data
                }
            } label: {
                ZStack {
                    Circle()
                        .strokeBorder(Color.white, lineWidth: 4)
                        .frame(width: 72, height: 72)
                    Circle()
                        .fill(Color.white)
                        .frame(width: 58, height: 58)
                }
            }
            .accessibilityLabel("Take photo")

            HStack {
                Button("Choose from library") { showLibraryPicker = true }
                    .font(.subheadline)
                    .foregroundColor(.white)

                Spacer()

                Button("Skip this shot") { advance() }
                    .font(.subheadline)
                    .foregroundColor(.white.opacity(0.8))
            }
            .padding(.horizontal, 24)
        }
        .padding(.bottom, 28)
    }

    // MARK: - Freeze-frame review

    private func reviewScreen(image: UIImage) -> some View {
        ZStack {
            Image(uiImage: image)
                .resizable()
                .scaledToFit()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .ignoresSafeArea()

            VStack {
                Spacer()
                HStack(spacing: 12) {
                    Button {
                        reviewData = nil
                    } label: {
                        Label("Retake", systemImage: "arrow.counterclockwise")
                            .font(.headline)
                            .foregroundColor(.white)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(Color.white.opacity(0.2))
                            .cornerRadius(12)
                    }

                    Button {
                        if let data = reviewData {
                            reviewData = nil
                            acceptShot(data)
                        }
                    } label: {
                        Label("Use photo", systemImage: "checkmark")
                            .font(.headline)
                            .foregroundColor(.black)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(Color.white)
                            .cornerRadius(12)
                    }
                }
                .padding(.horizontal, 20)
                .padding(.bottom, 28)
            }
        }
    }

    // MARK: - No-camera / no-permission fallback

    private var fallbackScreen: some View {
        VStack(spacing: 16) {
            header

            Spacer()

            Image(systemName: currentSlot.map(overlaySymbol(for:)) ?? "car.fill")
                .resizable()
                .scaledToFit()
                .frame(maxWidth: 180, maxHeight: 120)
                .foregroundColor(.white.opacity(0.4))

            if let slot = currentSlot {
                Text("Shot \(currentIndex + 1) of \(slots.count) — \(slot.displayLabel)")
                    .font(.headline)
                    .foregroundColor(.white)
                Text(slot.guidedHint)
                    .font(.caption)
                    .foregroundColor(.white.opacity(0.8))
            }

            Text(fallbackMessage)
                .font(.subheadline)
                .foregroundColor(.white.opacity(0.7))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)

            if camera.status == .denied {
                Button("Open Settings") {
                    if let url = URL(string: UIApplication.openSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                }
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.white)
            }

            Button {
                showLibraryPicker = true
            } label: {
                Label("Choose from library", systemImage: "photo.on.rectangle")
                    .font(.headline)
                    .foregroundColor(.black)
                    .padding(.horizontal, 24)
                    .padding(.vertical, 12)
                    .background(Color.white)
                    .cornerRadius(12)
            }

            Button("Skip this shot") { advance() }
                .font(.subheadline)
                .foregroundColor(.white.opacity(0.8))

            progressDots
            thumbnailStrip

            Spacer()
        }
    }

    private var fallbackMessage: String {
        switch camera.status {
        case .denied:
            return "Camera access is turned off for DriveBai. Allow it in Settings, or pick each shot from your photo library."
        case .unavailable:
            return "No camera is available on this device. Pick each shot from your photo library instead."
        default:
            return "Starting camera…"
        }
    }

    // MARK: - Flow

    private func acceptShot(_ data: Data) {
        if let slot = currentSlot {
            captured[slot] = data
        }
        advance()
    }

    private func advance() {
        if currentIndex + 1 < slots.count {
            currentIndex += 1
        } else {
            finish()
        }
    }

    private func finish() {
        guard !didComplete else { return }
        didComplete = true
        camera.stop()
        onComplete(captured)
        dismiss()
    }

    private func overlaySymbol(for slot: PhotoSlotType) -> String {
        // Guard against SF Symbols missing on older runtimes — fall back to
        // the plain car glyph rather than rendering nothing.
        UIImage(systemName: slot.overlayAssetName) != nil ? slot.overlayAssetName : "car.fill"
    }

    /// Re-encodes an arbitrary library pick as JPEG so the upload pipeline
    /// always receives a format the backend accepts (HEIC → JPEG).
    private static func normalizedJPEG(from data: Data) -> Data? {
        guard let image = UIImage(data: data) else { return nil }
        return image.jpegData(compressionQuality: 0.85)
    }
}

// MARK: - Camera controller

/// Owns the AVCaptureSession lifecycle on a background queue and exposes a
/// tiny async-ish surface: `start()`, `stop()`, `capture(_:)`.
final class GuidedCameraController: NSObject, ObservableObject {
    enum Status: Equatable {
        case idle
        case starting
        case running
        case unavailable
        case denied
    }

    @Published var status: Status = .idle

    let session = AVCaptureSession()

    private let sessionQueue = DispatchQueue(label: "com.drivebai.guided-camera")
    private let photoOutput = AVCapturePhotoOutput()
    private var isConfigured = false
    private var pendingCapture: ((Data?) -> Void)?

    func start() {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            startConfigured()
        case .notDetermined:
            status = .starting
            AVCaptureDevice.requestAccess(for: .video) { [weak self] granted in
                DispatchQueue.main.async {
                    if granted {
                        self?.startConfigured()
                    } else {
                        self?.status = .denied
                    }
                }
            }
        case .denied, .restricted:
            status = .denied
        @unknown default:
            status = .denied
        }
    }

    private func startConfigured() {
        status = .starting
        sessionQueue.async { [weak self] in
            guard let self else { return }
            let ok = self.configureIfNeeded()
            if ok {
                if !self.session.isRunning {
                    self.session.startRunning()
                }
                DispatchQueue.main.async { self.status = .running }
            } else {
                DispatchQueue.main.async { self.status = .unavailable }
            }
        }
    }

    func stop() {
        sessionQueue.async { [weak self] in
            guard let self else { return }
            if self.session.isRunning {
                self.session.stopRunning()
            }
        }
    }

    /// Takes a photo; completion is delivered on the main queue with JPEG
    /// data (nil on failure).
    func capture(completion: @escaping (Data?) -> Void) {
        sessionQueue.async { [weak self] in
            guard let self, self.isConfigured, self.session.isRunning else {
                DispatchQueue.main.async { completion(nil) }
                return
            }
            let settings = AVCapturePhotoSettings()
            self.pendingCapture = completion
            self.photoOutput.capturePhoto(with: settings, delegate: self)
        }
    }

    // Runs on sessionQueue.
    private func configureIfNeeded() -> Bool {
        if isConfigured { return true }
        guard let device = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back),
              let input = try? AVCaptureDeviceInput(device: device) else {
            return false
        }

        session.beginConfiguration()
        session.sessionPreset = .photo
        guard session.canAddInput(input), session.canAddOutput(photoOutput) else {
            session.commitConfiguration()
            return false
        }
        session.addInput(input)
        session.addOutput(photoOutput)
        session.commitConfiguration()
        isConfigured = true
        return true
    }
}

extension GuidedCameraController: AVCapturePhotoCaptureDelegate {
    func photoOutput(
        _ output: AVCapturePhotoOutput,
        didFinishProcessingPhoto photo: AVCapturePhoto,
        error: Error?
    ) {
        let completion = pendingCapture
        pendingCapture = nil

        guard error == nil, let raw = photo.fileDataRepresentation() else {
            DispatchQueue.main.async { completion?(nil) }
            return
        }
        // Normalize to JPEG 0.85 so every capture path emits the same format
        // the multipart upload endpoint accepts.
        let jpeg = UIImage(data: raw)?.jpegData(compressionQuality: 0.85) ?? raw
        DispatchQueue.main.async { completion?(jpeg) }
    }
}

// MARK: - Preview layer host

private struct CameraPreviewView: UIViewRepresentable {
    let session: AVCaptureSession

    final class PreviewHostView: UIView {
        override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }
        var previewLayer: AVCaptureVideoPreviewLayer {
            layer as! AVCaptureVideoPreviewLayer
        }
    }

    func makeUIView(context: Context) -> PreviewHostView {
        let view = PreviewHostView()
        view.previewLayer.session = session
        view.previewLayer.videoGravity = .resizeAspectFill
        return view
    }

    func updateUIView(_ uiView: PreviewHostView, context: Context) {
        if uiView.previewLayer.session !== session {
            uiView.previewLayer.session = session
        }
    }
}
