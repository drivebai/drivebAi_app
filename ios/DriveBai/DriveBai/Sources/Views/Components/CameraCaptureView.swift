import SwiftUI
import UIKit

// MARK: - Camera Capture (single shot)
//
// Minimal `UIImagePickerController(.camera)` wrapper used by
// `DocumentSourcePicker` for the "Take Photo" path (QA pt 1). Returns the
// captured shot as JPEG data (0.85 compression) via the completion; nil on
// cancel. Only present this when
// `UIImagePickerController.isSourceTypeAvailable(.camera)` — on the
// simulator the source picker degrades to Photo Library / Files.
//
// When `onLibraryRequested` is set, a "Library" button floats bottom-left
// INSIDE the open camera (client ask, reported 3x: the photo library must be
// reachable from the camera itself, not only from the chooser shown before
// it). The stock camera controls keep working — the overlay passes every
// touch through except on the button itself. The callback is expected to
// dismiss the camera and open the library; presentation sequencing lives
// with the caller (see DocumentSourcePicker's fullScreenCover onDismiss).
//
// `NSCameraUsageDescription` is already configured in the project
// (INFOPLIST_KEY_NSCameraUsageDescription).

struct CameraCaptureView: UIViewControllerRepresentable {
    /// Called exactly once — with JPEG data on capture, nil on cancel.
    let onCapture: (Data?) -> Void
    /// When set, renders the in-camera "Library" button (bottom-left, above
    /// the stock control bar) and calls this on tap.
    var onLibraryRequested: (() -> Void)? = nil

    static var isCameraAvailable: Bool {
        UIImagePickerController.isSourceTypeAvailable(.camera)
    }

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.cameraCaptureMode = .photo
        picker.allowsEditing = false
        picker.delegate = context.coordinator

        if let onLibraryRequested {
            // Passthrough overlay: only the button is hit-testable, so the
            // stock shutter / Cancel / flip controls are untouched.
            let overlay = PassthroughOverlayView(frame: UIScreen.main.bounds)
            overlay.autoresizingMask = [.flexibleWidth, .flexibleHeight]

            var config = UIButton.Configuration.filled()
            config.image = UIImage(systemName: "photo.on.rectangle")
            config.title = "Library"
            config.imagePadding = 6
            config.baseBackgroundColor = UIColor.black.withAlphaComponent(0.55)
            config.baseForegroundColor = .white
            config.cornerStyle = .capsule
            config.contentInsets = NSDirectionalEdgeInsets(top: 10, leading: 14, bottom: 10, trailing: 14)
            let button = UIButton(configuration: config, primaryAction: UIAction { _ in
                onLibraryRequested()
            })
            button.translatesAutoresizingMaskIntoConstraints = false
            overlay.addSubview(button)
            NSLayoutConstraint.activate([
                button.leadingAnchor.constraint(equalTo: overlay.safeAreaLayoutGuide.leadingAnchor, constant: 16),
                // Clear of the stock bottom control bar (~120pt) on all sizes.
                button.bottomAnchor.constraint(equalTo: overlay.safeAreaLayoutGuide.bottomAnchor, constant: -128),
                button.heightAnchor.constraint(greaterThanOrEqualToConstant: 44),
            ])
            picker.cameraOverlayView = overlay
        }

        return picker
    }

    func updateUIViewController(_ uiViewController: UIImagePickerController, context: Context) {}

    func makeCoordinator() -> Coordinator {
        Coordinator(onCapture: onCapture)
    }

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let onCapture: (Data?) -> Void

        init(onCapture: @escaping (Data?) -> Void) {
            self.onCapture = onCapture
        }

        func imagePickerController(
            _ picker: UIImagePickerController,
            didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]
        ) {
            let image = (info[.originalImage] as? UIImage)
            let data = image?.jpegData(compressionQuality: 0.85)
            onCapture(data)
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
            onCapture(nil)
        }
    }
}

/// Overlay that is invisible to touches except where a subview (the Library
/// button) is hit — everything else falls through to the camera controls.
private final class PassthroughOverlayView: UIView {
    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
        let view = super.hitTest(point, with: event)
        return view === self ? nil : view
    }
}
