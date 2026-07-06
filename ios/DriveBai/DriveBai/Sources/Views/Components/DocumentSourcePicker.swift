import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

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
                    guard let data = try? await item.loadTransferable(type: Data.self) else { return }
                    let mimeType: String
                    let ext: String
                    if let contentType = item.supportedContentTypes.first, contentType.conforms(to: .png) {
                        mimeType = "image/png"
                        ext = "png"
                    } else {
                        mimeType = "image/jpeg"
                        ext = "jpg"
                    }
                    let picked = PickedDocument(
                        data: data,
                        filename: "\(filenameBase).\(ext)",
                        mimeType: mimeType
                    )
                    await MainActor.run { onPicked(picked) }
                }
            }
            .fileImporter(
                isPresented: $showFilePicker,
                allowedContentTypes: [.pdf, .jpeg, .png, .image],
                allowsMultipleSelection: false
            ) { result in
                guard case .success(let urls) = result, let url = urls.first else { return }
                guard url.startAccessingSecurityScopedResource() else { return }
                defer { url.stopAccessingSecurityScopedResource() }
                guard let data = try? Data(contentsOf: url) else { return }
                onPicked(PickedDocument(
                    data: data,
                    filename: url.lastPathComponent,
                    mimeType: DocumentMIME.forURL(url)
                ))
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
