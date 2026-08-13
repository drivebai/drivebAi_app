import SwiftUI

/// Sheet for editing the authenticated user's profile.
///
/// Name saves directly via PATCH /profile. Email and phone are IDENTIFIERS
/// (batch items 7+8): changing either requires a 6-digit confirmation code —
/// for email the code goes to the NEW address (proving ownership; the
/// committed email is also marked verified), for phone it goes to the
/// account's current email (proving account control — there is no SMS
/// channel). Nothing commits until the code verifies, and both changes
/// still pass the uniqueness checks. One identifier change at a time.
struct EditProfileView: View {
    @EnvironmentObject private var authStore: AuthStore
    @Environment(\.dismiss) private var dismiss

    @State private var firstName: String = ""
    @State private var lastName: String = ""
    @State private var email: String = ""
    @State private var phone: String = ""
    @State private var isSaving = false
    @State private var errorMessage: String?

    /// Set once the server has sent a confirmation code; drives the
    /// code-entry sheet.
    @State private var pendingChange: PendingContactChange?

    private let originalFirstName: String
    private let originalLastName: String
    private let originalEmail: String
    private let originalPhone: String

    struct PendingContactChange: Identifiable {
        let id = UUID()
        let field: String
        let sentTo: String
        let newValue: String
    }

    init(user: UserProfile) {
        _firstName = State(initialValue: user.firstName)
        _lastName = State(initialValue: user.lastName)
        _email = State(initialValue: user.email)
        _phone = State(initialValue: user.phone ?? "")
        self.originalFirstName = user.firstName
        self.originalLastName = user.lastName
        self.originalEmail = user.email
        self.originalPhone = user.phone ?? ""
    }

    private var emailChanged: Bool {
        email.trimmed.lowercased() != originalEmail.lowercased()
    }
    private var phoneChanged: Bool { phone.trimmed != originalPhone }
    private var nameChanged: Bool {
        firstName.trimmed != originalFirstName || lastName.trimmed != originalLastName
    }
    private var hasChanges: Bool { nameChanged || emailChanged || phoneChanged }

    /// Backend rejects empty first/last name; mirror that gate locally so
    /// we don't bother the API and so the disabled state on Save is honest.
    private var isFormValid: Bool {
        !firstName.trimmed.isEmpty && !lastName.trimmed.isEmpty
            && email.trimmed.contains("@")
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

                Section {
                    TextField("Email", text: $email)
                        .textContentType(.emailAddress)
                        .keyboardType(.emailAddress)
                        .autocapitalization(.none)
                        .autocorrectionDisabled()
                    TextField("Phone number", text: $phone)
                        .textContentType(.telephoneNumber)
                        .keyboardType(.phonePad)
                } header: {
                    Text("Contact")
                } footer: {
                    Text("Changing your email or phone requires a confirmation code — we'll send it before anything changes.")
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
            .sheet(item: $pendingChange) { change in
                ContactChangeCodeSheet(
                    change: change,
                    onVerified: {
                        Task {
                            await authStore.refreshCurrentUser()
                            pendingChange = nil
                            dismiss()
                        }
                    },
                    onCancelled: { pendingChange = nil }
                )
            }
        }
    }

    private func save() async {
        errorMessage = nil

        if emailChanged && phoneChanged {
            errorMessage = "Change your email and phone one at a time — save one, then the other."
            return
        }

        isSaving = true
        defer { isSaving = false }

        do {
            // 1) Names save directly (absent fields = unchanged).
            if nameChanged {
                let req = UpdateProfileRequest(
                    role: nil,
                    firstName: firstName.trimmed == originalFirstName ? nil : firstName.trimmed,
                    lastName: lastName.trimmed == originalLastName ? nil : lastName.trimmed,
                    phone: nil
                )
                _ = try await APIClient.shared.updateProfile(request: req)
            }

            // 2) An identifier change starts the OTP flow — the code sheet
            //    takes over; nothing commits until it verifies.
            if emailChanged || phoneChanged {
                let field = emailChanged ? "email" : "phone"
                let value = emailChanged ? email.trimmed : phone.trimmed
                let resp = try await APIClient.shared.requestContactChange(field: field, newValue: value)
                pendingChange = PendingContactChange(field: field, sentTo: resp.sentTo, newValue: value)
            } else {
                await authStore.refreshCurrentUser()
                dismiss()
            }
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

// MARK: - Code entry (batch item 8)

/// Enter the 6-digit code that confirms an email/phone change. Mirrors the
/// login-OTP budget server-side: 10-minute expiry, 5 attempts, resendable.
private struct ContactChangeCodeSheet: View {
    let change: EditProfileView.PendingContactChange
    let onVerified: () -> Void
    let onCancelled: () -> Void

    @State private var code = ""
    @State private var isVerifying = false
    @State private var isResending = false
    @State private var errorMessage: String?
    @State private var resent = false
    @FocusState private var codeFocused: Bool

    private var explainer: String {
        change.field == "email"
            ? "We sent a 6-digit code to \(change.sentTo) — your new email. Enter it to prove the address is yours."
            : "We sent a 6-digit code to \(change.sentTo) — your account email. Enter it to confirm the phone change."
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 20) {
                Image(systemName: "envelope.badge.shield.half.filled")
                    .font(.system(size: 44))
                    .foregroundColor(.driveBaiPrimary)
                    .padding(.top, 32)

                Text("Confirm your change")
                    .font(.title3.weight(.bold))

                Text(explainer)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)

                TextField("6-digit code", text: $code)
                    .keyboardType(.numberPad)
                    .textContentType(.oneTimeCode)
                    .multilineTextAlignment(.center)
                    .font(.title2.monospacedDigit())
                    .padding()
                    .background(Color(.systemGray6))
                    .cornerRadius(12)
                    .padding(.horizontal, 40)
                    .focused($codeFocused)
                    .onChange(of: code) { _, newValue in
                        code = String(newValue.filter(\.isNumber).prefix(6))
                    }

                if let errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundColor(.red)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 24)
                }

                Button(action: { Task { await verify() } }) {
                    if isVerifying {
                        ProgressView().tint(.white)
                    } else {
                        Text("Confirm")
                    }
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(code.count != 6 || isVerifying)
                .padding(.horizontal, 24)

                Button(resent ? "Code re-sent" : "Send a new code") {
                    Task { await resend() }
                }
                .font(.footnote)
                .disabled(isResending || resent)

                Spacer()
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancelled() }
                        .disabled(isVerifying)
                }
            }
            .onAppear { codeFocused = true }
            .interactiveDismissDisabled(isVerifying)
        }
    }

    @MainActor
    private func verify() async {
        isVerifying = true
        errorMessage = nil
        defer { isVerifying = false }
        do {
            _ = try await APIClient.shared.verifyContactChange(code: code)
            onVerified()
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
            code = ""
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    @MainActor
    private func resend() async {
        isResending = true
        defer { isResending = false }
        do {
            _ = try await APIClient.shared.requestContactChange(field: change.field, newValue: change.newValue)
            resent = true
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
