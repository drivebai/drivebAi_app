import SwiftUI
import PDFKit

/// Thin SwiftUI bridge over UIKit's PDFView. Renders a single page-at-a-time
/// continuous scroll suitable for typical document attachments.
struct PDFKitRepresentedView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> PDFView {
        let view = PDFView()
        view.autoScales = true
        view.displayMode = .singlePageContinuous
        view.displayDirection = .vertical
        view.backgroundColor = .systemBackground
        view.pageShadowsEnabled = false
        view.document = PDFDocument(url: url)
        return view
    }

    func updateUIView(_ view: PDFView, context: Context) {
        // Only swap the document if the URL changed — avoids resetting scroll
        // position on every SwiftUI redraw.
        if view.document?.documentURL != url {
            view.document = PDFDocument(url: url)
        }
    }
}
