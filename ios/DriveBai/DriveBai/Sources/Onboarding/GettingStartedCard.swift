import SwiftUI

// MARK: - Checklist item
//
// Each row is bound to a REAL domain predicate (documents present, a car
// exists, a lease exists, a car is live) — never a "you saw a tooltip" flag.
// Callers pass the resolved booleans; this component only renders.

struct ChecklistItem: Identifiable, Equatable {
    let id: String
    let title: String
    let isDone: Bool

    init(_ id: String, _ title: String, isDone: Bool) {
        self.id = id
        self.title = title
        self.isDone = isDone
    }
}

// MARK: - ChecklistCard
//
// The role-scoped "Getting started" card. Collapsible, dismissible, and
// auto-hiding at 100% complete. State (collapsed / dismissed) is owned by the
// caller so it can be persisted in the tour cache blob; sensible local defaults
// let it work standalone (and in previews).

struct ChecklistCard: View {
    let title: String
    let subtitle: String?
    let items: [ChecklistItem]

    /// Collapsed state, persisted by the caller when bound.
    @Binding var collapsed: Bool
    /// Invoked when the user taps "Dismiss".
    var onDismiss: (() -> Void)?
    /// Optional primary CTA (e.g. tap the first incomplete step).
    var onPrimaryAction: (() -> Void)?
    var primaryActionTitle: String?

    init(
        title: String,
        subtitle: String? = nil,
        items: [ChecklistItem],
        collapsed: Binding<Bool> = .constant(false),
        primaryActionTitle: String? = nil,
        onPrimaryAction: (() -> Void)? = nil,
        onDismiss: (() -> Void)? = nil
    ) {
        self.title = title
        self.subtitle = subtitle
        self.items = items
        self._collapsed = collapsed
        self.primaryActionTitle = primaryActionTitle
        self.onPrimaryAction = onPrimaryAction
        self.onDismiss = onDismiss
    }

    private var completedCount: Int { items.filter(\.isDone).count }
    private var allComplete: Bool { !items.isEmpty && completedCount == items.count }

    var body: some View {
        // Auto-hide at 100%.
        if allComplete {
            EmptyView()
        } else {
            card
        }
    }

    private var card: some View {
        VStack(alignment: .leading, spacing: 14) {
            header

            if !collapsed {
                VStack(spacing: 12) {
                    ForEach(items) { item in
                        row(item)
                    }
                }

                if let subtitle {
                    Text(subtitle)
                        .font(.footnote)
                        .foregroundColor(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }

                if let primaryActionTitle, let onPrimaryAction {
                    Button(action: onPrimaryAction) {
                        Text(primaryActionTitle)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.driveBaiPrimary)
                }
            }
        }
        .padding(16)
        .background(
            RoundedRectangle(cornerRadius: 16)
                .fill(Color(.secondarySystemBackground))
        )
        .accessibilityElement(children: .contain)
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.headline)
                Text("\(completedCount) of \(items.count) done")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer(minLength: 8)

            if let onDismiss {
                Menu {
                    Button(role: .destructive, action: onDismiss) {
                        Label("Dismiss", systemImage: "xmark.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundColor(.secondary)
                        .frame(width: 32, height: 32)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("More options")
            }

            Button {
                withAnimation(.easeInOut(duration: 0.2)) { collapsed.toggle() }
            } label: {
                Image(systemName: collapsed ? "chevron.down" : "chevron.up")
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(.secondary)
                    .frame(width: 32, height: 32)
                    .contentShape(Rectangle())
            }
            .accessibilityLabel(collapsed ? "Expand checklist" : "Collapse checklist")
        }
    }

    private func row(_ item: ChecklistItem) -> some View {
        HStack(spacing: 12) {
            Image(systemName: item.isDone ? "checkmark.circle.fill" : "circle")
                .font(.system(size: 20))
                .foregroundColor(item.isDone ? .driveBaiPrimary : .secondary.opacity(0.5))
            Text(item.title)
                .font(.subheadline)
                .strikethrough(item.isDone, color: .secondary)
                .foregroundColor(item.isDone ? .secondary : .primary)
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(item.title). \(item.isDone ? "Done" : "Not done").")
    }
}

// MARK: - Role convenience builders
//
// Wave-2 renders these in the exact empty-state gaps. Each item maps to Section
// 8's domain predicate; callers pass the live booleans.

extension ChecklistCard {

    /// Driver "Getting started" checklist (Section 8).
    static func driver(
        hasLicense: Bool,
        browsedCars: Bool,
        sentRequest: Bool,
        foundWhereToPay: Bool,
        collapsed: Binding<Bool> = .constant(false),
        onPrimaryAction: (() -> Void)? = nil,
        onDismiss: (() -> Void)? = nil
    ) -> ChecklistCard {
        ChecklistCard(
            title: "Getting started",
            subtitle: "A few steps to your first rental.",
            items: [
                ChecklistItem("license", "Add your driver's license", isDone: hasLicense),
                ChecklistItem("browse", "Browse cars", isDone: browsedCars),
                ChecklistItem("request", "Send a rental request", isDone: sentRequest),
                ChecklistItem("pay", "Find where you pay", isDone: foundWhereToPay)
            ],
            collapsed: collapsed,
            // Only offer the CTA when a caller supplied something for it to do.
            // The card's own guard needs both, so a title without an action
            // rendered nothing at all.
            primaryActionTitle: (!browsedCars && onPrimaryAction != nil) ? "Discover cars" : nil,
            onPrimaryAction: onPrimaryAction,
            onDismiss: onDismiss
        )
    }

    /// Owner "Getting started" checklist (Section 8).
    static func owner(
        hasCar: Bool,
        docsComplete: Bool,
        submittedForReview: Bool,
        isLive: Bool,
        collapsed: Binding<Bool> = .constant(false),
        onPrimaryAction: (() -> Void)? = nil,
        onDismiss: (() -> Void)? = nil
    ) -> ChecklistCard {
        ChecklistCard(
            title: "Getting started",
            subtitle: "List your first car and get approved.",
            items: [
                ChecklistItem("list", "List your first car", isDone: hasCar),
                ChecklistItem("docs", "Complete required documents", isDone: docsComplete),
                ChecklistItem("review", "Submitted for review", isDone: submittedForReview),
                ChecklistItem("live", "You're live", isDone: isLive)
            ],
            collapsed: collapsed,
            primaryActionTitle: hasCar ? nil : "List your first vehicle",
            onPrimaryAction: onPrimaryAction,
            onDismiss: onDismiss
        )
    }
}

// MARK: - Previews

#if DEBUG
#Preview("Driver checklist · in progress") {
    ScrollView {
        StatefulPreviewWrapper(false) { collapsed in
            ChecklistCard.driver(
                hasLicense: true,
                browsedCars: true,
                sentRequest: false,
                foundWhereToPay: false,
                collapsed: collapsed,
                onPrimaryAction: {},
                onDismiss: {}
            )
            .padding()
        }
    }
}

#Preview("Owner checklist · zero cars") {
    ScrollView {
        StatefulPreviewWrapper(false) { collapsed in
            ChecklistCard.owner(
                hasCar: false,
                docsComplete: false,
                submittedForReview: false,
                isLive: false,
                collapsed: collapsed,
                onPrimaryAction: {},
                onDismiss: {}
            )
            .padding()
        }
    }
}

#Preview("Checklist · collapsed") {
    ScrollView {
        StatefulPreviewWrapper(true) { collapsed in
            ChecklistCard.owner(
                hasCar: true,
                docsComplete: true,
                submittedForReview: false,
                isLive: false,
                collapsed: collapsed,
                onDismiss: {}
            )
            .padding()
        }
    }
}

/// Tiny helper so previews can drive a `@Binding`.
private struct StatefulPreviewWrapper<Content: View>: View {
    @State private var value: Bool
    let content: (Binding<Bool>) -> Content
    init(_ initial: Bool, @ViewBuilder content: @escaping (Binding<Bool>) -> Content) {
        _value = State(initialValue: initial)
        self.content = content
    }
    var body: some View { content($value) }
}
#endif
