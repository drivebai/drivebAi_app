import SwiftUI

/// Sheet for editing the authenticated user's profile.
///
/// Surfaces ONLY the fields the backend's PATCH /api/v1/profile is willing
/// to write for a self-edit: first name, last name, phone. Email, role,
/// onboarding status, and verification flags are read-only here because
/// they either need OTP re-verification (email), have a dedicated
/// mode-switch flow (role), or are server-managed (everything else).
///
/// On Save:
///   - validates + trims locally
///   - calls APIClient.updateProfile
///   - tells AuthStore to refresh `state.user` so every other screen
///     (chat header, Today, counterparty profile) picks up the new values
///   - dismisses on success; shows an inline error on failure (the sheet
///     stays open so the user can fix and retry without re-typing).
struct EditProfileView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    @State private var firstName: String = ""
    @State private var lastName: String = ""
    @State private var phone: String = ""
    @State private var isSaving = false
    @State private var errorMessage: String?

    /// Original snapshot — used to detect "no changes" and to decide which
    /// fields to send. Avoids hitting the API with a payload that just
    /// re-sets the same values.
    private let originalFirstName: String
    private let originalLastName: String
    private let originalPhone: String

    init(user: UserProfile) {
        _firstName = State(initialValue: user.firstName)
        _lastName = State(initialValue: user.lastName)
        _phone = State(initialValue: user.phone ?? "")
        self.originalFirstName = user.firstName
        self.originalLastName = user.lastName
        self.originalPhone = user.phone ?? ""
    }

    private var hasChanges: Bool {
        firstName.trimmed != originalFirstName ||
        lastName.trimmed != originalLastName ||
        phone.trimmed != originalPhone
    }

    /// Backend rejects empty first/last name; mirror that gate locally so
    /// we don't bother the API and so the disabled state on Save is honest.
    private var isFormValid: Bool {
        !firstName.trimmed.isEmpty && !lastName.trimmed.isEmpty
    }

    var body: some View {
        NavigationStack {
            Form {
                Section(header: Text("Name")) {
                    TextField("First name", text: $firstName)
                        .textContentType(.givenName)
                        .autocapitalization(.words)
                    TextField("Last name", text: $lastName)
                        .textContentType(.familyName)
                        .autocapitalization(.words)
                }

                Section(header: Text("Contact")) {
                    TextField("Phone number", text: $phone)
                        .textContentType(.telephoneNumber)
                        .keyboardType(.phonePad)
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundColor(.red)
                    }
                }
            }
            .navigationTitle("Edit profile")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSaving)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if isSaving {
                        ProgressView()
                    } else {
                        Button("Save") { Task { await save() } }
                            .disabled(!isFormValid || !hasChanges)
                    }
                }
            }
            .interactiveDismissDisabled(isSaving)
        }
    }

    private func save() async {
        errorMessage = nil
        isSaving = true
        defer { isSaving = false }

        // Only send fields that actually changed — server treats absent
        // fields as "leave unchanged", so this keeps the payload tight.
        let req = UpdateProfileRequest(
            role: nil,
            firstName: firstName.trimmed == originalFirstName ? nil : firstName.trimmed,
            lastName:  lastName.trimmed  == originalLastName  ? nil : lastName.trimmed,
            phone:     phone.trimmed     == originalPhone     ? nil : phone.trimmed
        )

        do {
            _ = try await APIClient.shared.updateProfile(request: req)
            // Fresh /me read keeps AuthStore the single source of truth —
            // no risk of state drift between the response object and what
            // the rest of the app sees.
            await authStore.refreshCurrentUser()
            dismiss()
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

private extension String {
    var trimmed: String { trimmingCharacters(in: .whitespacesAndNewlines) }
}
