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
// `NSCameraUsageDescription` is already configured in the project
// (INFOPLIST_KEY_NSCameraUsageDescription).

struct CameraCaptureView: UIViewControllerRepresentable {
    /// Called exactly once — with JPEG data on capture, nil on cancel.
    let onCapture: (Data?) -> Void

    static var isCameraAvailable: Bool {
        UIImagePickerController.isSourceTypeAvailable(.camera)
    }

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.cameraCaptureMode = .photo
        picker.allowsEditing = false
        picker.delegate = context.coordinator
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
