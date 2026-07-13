import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import UIKit

// MARK: - Picked Document
//
// Output of every document/photo source flow: raw bytes plus the filename
// and MIME type the multipart upload endpoints expect.

struct PickedDocument: Equatable {
    let data: Data
    let filename: String
    let mimeType: String
}

// MARK: - Shared MIME helper
//
// The one MIME-for-extension mapping (QA pt 1). Replaces the private copies
// scattered across DocumentUploadView / CreateListingFlowView /
// CarDetailEditView — new code should call this instead of adding a fourth.

enum DocumentMIME {
    static func forExtension(_ ext: String) -> String {
        switch ext.lowercased() {
        case "pdf": return "application/pdf"
        case "png": return "image/png"
        case "jpg", "jpeg": return "image/jpeg"
        case "heic": return "image/heic"
        default: return "application/octet-stream"
        }
    }

    static func forURL(_ url: URL) -> String {
        forExtension(url.pathExtension)
    }
}

// MARK: - Shared image transcode helper
//
// Single JPEG normalizer used by every "pick a photo/document" path so HEIC
// and other camera formats can't reach the multipart upload as-is and get
// rejected by the server MIME whitelist (image/jpeg, image/png,
// application/pdf). Mirrors CameraCaptureView's jpegData(0.85).

enum ImageTranscode {
    /// Re-encodes arbitrary image bytes (HEIC, PNG, TIFF, …) as JPEG.
    /// Returns nil when the bytes are not a decodable image.
    static func jpeg(from data: Data, compressionQuality: CGFloat = 0.85) -> Data? {
        UIImage(data: data)?.jpegData(compressionQuality: compressionQuality)
    }
}

// MARK: - Document Source Picker
//
// Reusable "where's the file coming from?" chooser (QA pt 1):
//   - Take Photo   — UIImagePickerController camera, only offered when a
//                    camera exists (simulator degrades to the two below),
//   - Photo Library — PhotosPicker (.images),
//   - Files         — fileImporter (pdf / jpeg / png / image).
//
// Attach with `.documentSourcePicker(isPresented:filenameBase:onPicked:)`.
// Emits a `PickedDocument` on the main actor. This replaces the verbatim
// confirmationDialog + photosPicker + fileImporter blocks previously copied
// into DocumentUploadView, CreateListingFlowView and CarDetailEditView.

struct DocumentSourcePickerModifier: ViewModifier {
    @Binding var isPresented: Bool
    /// Base (extension-less) filename used for camera/library picks where
    /// the source has no meaningful filename, e.g. "registration" →
    /// "registration.jpg". Files picks keep their own filename.
    let filenameBase: String
    let onPicked: (PickedDocument) -> Void

    @State private var showCamera = false
    @State private var showPhotoPicker = false
    @State private var showFilePicker = false
    @State private var pickerItem: PhotosPickerItem?

    private var cameraAvailable: Bool {
        CameraCaptureView.isCameraAvailable
    }

    func body(content: Content) -> some View {
        content
            .confirmationDialog("Upload Document", isPresented: $isPresented, titleVisibility: .visible) {
                if cameraAvailable {
                    Button("Take Photo") { showCamera = true }
                }
                Button("Photo Library") { showPhotoPicker = true }
                Button("Files") { showFilePicker = true }
                Button("Cancel", role: .cancel) {}
            }
            .fullScreenCover(isPresented: $showCamera) {
                CameraCaptureView { data in
                    showCamera = false
                    guard let data else { return }
                    onPicked(PickedDocument(
                        data: data,
                        filename: "\(filenameBase).jpg",
                        mimeType: "image/jpeg"
                    ))
                }
                .ignoresSafeArea()
            }
            .photosPicker(isPresented: $showPhotoPicker, selection: $pickerItem, matching: .images)
            .onChange(of: pickerItem) { _, newItem in
                guard let item = newItem else { return }
                pickerItem = nil
                Task {
                    guard let data = try? await item.loadTransferable(type: Data.self),
                          // Transcode every image pick to JPEG so HEIC/other
                          // camera formats aren't mislabeled and rejected by the
                          // server MIME whitelist (image/jpeg, image/png,
                          // application/pdf).
                          let jpeg = ImageTranscode.jpeg(from: data) else { return }
                    let picked = PickedDocument(
                        data: jpeg,
                        filename: "\(filenameBase).jpg",
                        mimeType: "image/jpeg"
                    )
                    await MainActor.run { onPicked(picked) }
                }
            }
            .fileImporter(
                // Aligned to the server MIME whitelist: PDFs pass through and
                // any image (HEIC included) is transcoded to JPEG below.
                isPresented: $showFilePicker,
                allowedContentTypes: [.pdf, .image],
                allowsMultipleSelection: false
            ) { result in
                guard case .success(let urls) = result, let url = urls.first else { return }
                guard url.startAccessingSecurityScopedResource() else { return }
                defer { url.stopAccessingSecurityScopedResource() }
                guard let data = try? Data(contentsOf: url) else { return }

                if url.pathExtension.lowercased() == "pdf" {
                    // Leave PDFs untouched.
                    onPicked(PickedDocument(
                        data: data,
                        filename: url.lastPathComponent,
                        mimeType: "application/pdf"
                    ))
                } else if let jpeg = ImageTranscode.jpeg(from: data) {
                    // Transcode any image to JPEG (HEIC → JPEG).
                    let base = url.deletingPathExtension().lastPathComponent
                    onPicked(PickedDocument(
                        data: jpeg,
                        filename: "\(base).jpg",
                        mimeType: "image/jpeg"
                    ))
                } else {
                    // Neither a PDF nor a decodable image — best-effort raw
                    // passthrough so on-disk PNG/JPEG still work.
                    onPicked(PickedDocument(
                        data: data,
                        filename: url.lastPathComponent,
                        mimeType: DocumentMIME.forURL(url)
                    ))
                }
            }
    }
}

extension View {
    /// Presents the shared document source chooser (camera / library /
    /// files). See `DocumentSourcePickerModifier`.
    func documentSourcePicker(
        isPresented: Binding<Bool>,
        filenameBase: String,
        onPicked: @escaping (PickedDocument) -> Void
    ) -> some View {
        modifier(DocumentSourcePickerModifier(
            isPresented: isPresented,
            filenameBase: filenameBase,
            onPicked: onPicked
        ))
    }
}

// MARK: - Photo Source Picker (per-slot Take Photo / Choose from Library)
//
// Reusable per-slot add/replace chooser for VEHICLE photos (QA pt 4). Unlike
// DocumentSourcePicker — which also offers Files and returns a PickedDocument —
// this returns raw JPEG image `Data` ready to drop straight into a
// `CarPhotoSlot.localImageData`. Wired into CarPhotosEditView's grid tiles and
// exposed for the create-listing wizard grid (W2-A).
//
//   - Take Photo          — UIImagePickerController camera (CameraCaptureView),
//                           only offered when a camera exists,
//   - Choose from Library — PhotosPicker(.images), transcoded to JPEG.
//
// On the simulator (no camera) it skips the chooser and goes straight to the
// library so it stays usable and never crashes. The guided silhouette capture
// flow (GuidedPhotoCaptureView) is unaffected — this is only the per-slot
// add/replace path.
//
// Attach with `.photoSourcePicker(isPresented:onPicked:)`.

struct PhotoSourcePickerModifier: ViewModifier {
    @Binding var isPresented: Bool
    /// Delivered on the main actor with JPEG-encoded image data.
    let onPicked: (Data) -> Void

    @State private var showChooser = false
    @State private var showCamera = false
    @State private var showPhotoPicker = false
    @State private var pickerItem: PhotosPickerItem?

    private var cameraAvailable: Bool { CameraCaptureView.isCameraAvailable }

    func body(content: Content) -> some View {
        content
            .onChange(of: isPresented) { _, newValue in
                guard newValue else { return }
                // Consume the trigger, then route: show the full chooser when a
                // camera exists, otherwise jump straight to the library so the
                // simulator (no camera) degrades gracefully.
                isPresented = false
                if cameraAvailable {
                    showChooser = true
                } else {
                    showPhotoPicker = true
                }
            }
            .confirmationDialog("Add Photo", isPresented: $showChooser, titleVisibility: .visible) {
                Button("Take Photo") { showCamera = true }
                Button("Choose from Library") { showPhotoPicker = true }
                Button("Cancel", role: .cancel) {}
            }
            .fullScreenCover(isPresented: $showCamera) {
                CameraCaptureView { data in
                    showCamera = false
                    guard let data else { return }
                    // CameraCaptureView already emits JPEG 0.85.
                    onPicked(data)
                }
                .ignoresSafeArea()
            }
            .photosPicker(isPresented: $showPhotoPicker, selection: $pickerItem, matching: .images)
            .onChange(of: pickerItem) { _, newItem in
                guard let item = newItem else { return }
                pickerItem = nil
                Task {
                    guard let data = try? await item.loadTransferable(type: Data.self),
                          let jpeg = ImageTranscode.jpeg(from: data) else { return }
                    await MainActor.run { onPicked(jpeg) }
                }
            }
    }
}

extension View {
    /// Presents the reusable per-slot photo chooser (Take Photo / Choose from
    /// Library) and delivers JPEG image data. See `PhotoSourcePickerModifier`.
    func photoSourcePicker(
        isPresented: Binding<Bool>,
        onPicked: @escaping (Data) -> Void
    ) -> some View {
        modifier(PhotoSourcePickerModifier(isPresented: isPresented, onPicked: onPicked))
    }
}
