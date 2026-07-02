import SwiftUI
import UIKit

/// Signature capture surface for the Bill-of-Sale flow. Modeled after the
/// accident-report SignatureStepView so the interaction feels identical —
/// draw with a finger, Clear resets, Save renders a PNG and calls
/// `onSave(Data)`.
///
/// Kept file-scoped to the Purchase views so we don't risk breaking the
/// accident flow's private component.  Both views converge on the same
/// PNG rendering routine.
struct PurchaseSignaturePadView: View {
    let title: String
    let subtitle: String
    var isSaving: Bool = false
    let onSave: (Data) -> Void

    @State private var lines: [[CGPoint]] = []
    @State private var currentLine: [CGPoint] = []

    private var isEmpty: Bool { lines.isEmpty && currentLine.isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 4) {
                Text(title)
                    .font(.headline)
                Text(subtitle)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }

            // Canvas card
            ZStack {
                RoundedRectangle(cornerRadius: 16)
                    .fill(Color(.systemGray6))
                    .padding(4)

                RoundedRectangle(cornerRadius: 14)
                    .fill(Color(.systemBackground))
                    .overlay(
                        RoundedRectangle(cornerRadius: 14)
                            .stroke(isEmpty ? Color(.systemGray4) : Color.driveBaiPrimary.opacity(0.5),
                                    lineWidth: isEmpty ? 1 : 1.5)
                    )

                Canvas { ctx, _ in
                    for line in lines + [currentLine] {
                        guard line.count > 1 else { continue }
                        var path = Path()
                        path.move(to: line[0])
                        for pt in line.dropFirst() { path.addLine(to: pt) }
                        ctx.stroke(path, with: .color(.primary),
                                   style: StrokeStyle(lineWidth: 2.5, lineCap: .round, lineJoin: .round))
                    }
                }
                .gesture(
                    DragGesture(minimumDistance: 0)
                        .onChanged { val in currentLine.append(val.location) }
                        .onEnded { _ in lines.append(currentLine); currentLine = [] }
                )
                .cornerRadius(14)

                if isEmpty {
                    VStack(spacing: 8) {
                        Image(systemName: "pencil.line")
                            .font(.system(size: 38))
                            .foregroundColor(Color(.systemGray4))
                        Text("Sign here")
                            .font(.subheadline)
                            .foregroundColor(Color(.systemGray3))
                    }
                    .allowsHitTesting(false)
                }
            }
            .frame(height: 220)

            HStack(spacing: 12) {
                Button {
                    lines = []
                    currentLine = []
                } label: {
                    Text("Clear")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity, minHeight: 52)
                        .foregroundColor(isEmpty ? Color(.systemGray3) : .driveBaiPrimary)
                        .overlay(
                            RoundedRectangle(cornerRadius: 14)
                                .stroke(isEmpty ? Color(.systemGray4) : Color.driveBaiPrimary,
                                        lineWidth: 1.5)
                        )
                }
                .disabled(isEmpty)

                Button {
                    if let data = renderSignature(
                        lines: lines,
                        size: CGSize(width: 340, height: 220)
                    ) {
                        onSave(data)
                    }
                } label: {
                    HStack(spacing: 6) {
                        if isSaving { ProgressView().tint(.white).scaleEffect(0.8) }
                        Text(isSaving ? "Saving…" : "Save signature")
                            .font(.subheadline.weight(.semibold))
                    }
                    .frame(maxWidth: .infinity, minHeight: 52)
                    .background((isEmpty || isSaving) ? Color(.systemGray4) : Color.driveBaiPrimary)
                    .foregroundColor(.white)
                    .cornerRadius(14)
                }
                .disabled(isEmpty || isSaving)
            }
        }
    }

    private func renderSignature(lines: [[CGPoint]], size: CGSize) -> Data? {
        let renderer = UIGraphicsImageRenderer(size: size)
        return renderer.image { ctx in
            UIColor.white.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
            UIColor.black.setStroke()
            ctx.cgContext.setLineWidth(2.5)
            ctx.cgContext.setLineCap(.round)
            ctx.cgContext.setLineJoin(.round)
            for line in lines {
                guard line.count > 1 else { continue }
                ctx.cgContext.beginPath()
                ctx.cgContext.move(to: line[0])
                for pt in line.dropFirst() { ctx.cgContext.addLine(to: pt) }
                ctx.cgContext.strokePath()
            }
        }.pngData()
    }
}
