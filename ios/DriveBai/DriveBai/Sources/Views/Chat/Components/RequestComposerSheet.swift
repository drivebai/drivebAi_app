import SwiftUI

struct RequestComposerSheet: View {
    let chatId: UUID
    let userRole: UserRole
    let onCreated: () async -> Void

    @StateObject private var viewModel: RequestComposerViewModel
    @Environment(\.dismiss) private var dismiss

    init(chatId: UUID, userRole: UserRole, onCreated: @escaping () async -> Void) {
        self.chatId = chatId
        self.userRole = userRole
        self.onCreated = onCreated
        _viewModel = StateObject(wrappedValue: RequestComposerViewModel(chatId: chatId, userRole: userRole))
    }

    var body: some View {
        NavigationStack {
            Form {
                // Request type picker
                Section("Request Type") {
                    ForEach(viewModel.availableRequestTypes) { type in
                        Button {
                            viewModel.input.type = type
                            // Reset fields when switching type
                            viewModel.input.title = ""
                            viewModel.input.description = ""
                            viewModel.input.amount = nil
                        } label: {
                            HStack {
                                Image(systemName: type.iconName)
                                    .foregroundColor(.driveBaiPrimary)
                                    .frame(width: 28)
                                Text(type.displayTitle)
                                    .foregroundColor(.primary)
                                Spacer()
                                if viewModel.input.type == type {
                                    Image(systemName: "checkmark")
                                        .foregroundColor(.driveBaiPrimary)
                                }
                            }
                        }
                    }
                }

                // Type-specific form
                Section("Details") {
                    requestFormFields
                }

                if let error = viewModel.error {
                    Section {
                        Text(error)
                            .foregroundColor(.red)
                            .font(.caption)
                    }
                }
            }
            .navigationTitle("New Request")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Send") {
                        Task {
                            await viewModel.submit()
                        }
                    }
                    .disabled(!viewModel.input.isValid || viewModel.isSubmitting)
                }
            }
            .disabled(viewModel.isSubmitting)
            .overlay {
                if viewModel.isSubmitting {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(.ultraThinMaterial)
                }
            }
            .onChange(of: viewModel.didSubmit) { _, submitted in
                if submitted {
                    Task {
                        await onCreated()
                        dismiss()
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var requestFormFields: some View {
        switch viewModel.input.type {
        case .manualPayment, .additionalFee:
            PaymentRequestForm(input: $viewModel.input)
        case .delayedPayment:
            DelayedPaymentRequestForm(input: $viewModel.input)
        case .mechanicService:
            MechanicServiceRequestForm(input: $viewModel.input)
        case .generic:
            GenericRequestForm(input: $viewModel.input)
        }
    }
}
