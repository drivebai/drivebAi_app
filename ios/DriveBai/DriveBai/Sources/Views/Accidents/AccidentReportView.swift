import SwiftUI
import PhotosUI

// MARK: – Design tokens

private enum AD {
    static let radius: CGFloat    = 14
    static let cardPad: CGFloat   = 16
    static let sectionGap: CGFloat = 22
    static let fieldGap: CGFloat  = 12
    static let thumbSize: CGFloat = 96
    static let ctaHeight: CGFloat = 52
    static let progressH: CGFloat = 8
}

// MARK: – Root view

struct AccidentReportView: View {
    @StateObject private var vm: AccidentReportViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var showCloseConfirm = false

    init(relatedChatId: UUID? = nil, relatedCarId: UUID? = nil) {
        _vm = StateObject(wrappedValue: AccidentReportViewModel(
            relatedChatId: relatedChatId, relatedCarId: relatedCarId
        ))
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Color(.systemGroupedBackground).ignoresSafeArea()
                Group {
                    if vm.isLoading {
                        loadingView
                    } else if vm.isSubmitted {
                        submittedView
                    } else {
                        stepView
                    }
                }
            }
            .navigationTitle(vm.currentStep.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") {
                        if vm.isSubmitted {
                            dismiss()
                        } else {
                            showCloseConfirm = true
                        }
                    }
                }
            }
            .alert("Error", isPresented: Binding(
                get: { vm.error != nil },
                set: { if !$0 { vm.error = nil } }
            )) {
                Button("OK", role: .cancel) {}
            } message: { Text(vm.error ?? "") }
            .alert("Leave Report?", isPresented: $showCloseConfirm) {
                Button("Discard Draft", role: .destructive) { dismiss() }
                Button("Keep Editing", role: .cancel) {}
            } message: {
                Text("Your progress is saved as a draft and can be resumed from the chat.")
            }
        }
        .interactiveDismissDisabled(!vm.isSubmitted)
        .task { await vm.loadOrCreate() }
    }

    // MARK: Loading

    private var loadingView: some View {
        VStack(spacing: 16) {
            ProgressView().scaleEffect(1.3)
            Text("Creating report…")
                .font(.subheadline)
                .foregroundColor(.secondary)
        }
    }

    // MARK: Step wrapper

    @ViewBuilder
    private var stepView: some View {
        VStack(spacing: 0) {
            progressHeader
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: AD.sectionGap) {
                    stepContent
                }
                .padding(.horizontal, AD.cardPad)
                .padding(.vertical, AD.sectionGap)
                // extra bottom padding so content isn't hidden behind the CTA bar
                .padding(.bottom, AD.ctaHeight + 40)
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            ctaBar
        }
    }

    @ViewBuilder
    private var stepContent: some View {
        switch vm.currentStep {
        case .photos:      PhotosStepView(vm: vm)
        case .driver1:     DriverInfoStepView(info: $vm.driver1Info, label: "Driver 1")
        case .driver2:     Driver2StepView(vm: vm)
        case .damage:      DamageStepView(vm: vm)
        case .description: DescriptionStepView(text: $vm.accidentDescription)
        case .insurance:   InsuranceStepView(info: $vm.insuranceInfo)
        case .other:       OtherInfoStepView(info: $vm.otherInfo)
        case .signature:   SignatureStepView(vm: vm)
        case .review:      ReviewStepView(vm: vm)
        }
    }

    // MARK: Progress header

    private var progressHeader: some View {
        let total = AccidentStep.allCases.count
        let current = vm.currentStep.rawValue + 1
        let pct = CGFloat(current) / CGFloat(total)
        return VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Step \(current) of \(total)")
                    .font(.caption.weight(.semibold))
                    .foregroundColor(.secondary)
                Spacer()
                if vm.isSaving {
                    HStack(spacing: 5) {
                        ProgressView().scaleEffect(0.75)
                        Text("Saving…").font(.caption).foregroundColor(.secondary)
                    }
                }
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    RoundedRectangle(cornerRadius: AD.progressH / 2)
                        .fill(Color.driveBaiPrimary.opacity(0.15))
                        .frame(height: AD.progressH)
                    RoundedRectangle(cornerRadius: AD.progressH / 2)
                        .fill(Color.driveBaiPrimary)
                        .frame(width: geo.size.width * pct, height: AD.progressH)
                        .animation(.easeInOut(duration: 0.28), value: vm.currentStep.rawValue)
                }
            }
            .frame(height: AD.progressH)
        }
        .padding(.horizontal, AD.cardPad)
        .padding(.vertical, 12)
        .background(Color(.systemBackground))
    }

    // MARK: CTA bar

    private var ctaBar: some View {
        let isLast = vm.currentStep == .review
        let busy   = vm.isSaving || vm.isSubmitting
        return HStack(spacing: 12) {
            if vm.canGoBack {
                Button("Back") { vm.goBack() }
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(maxWidth: .infinity, minHeight: AD.ctaHeight)
                    .overlay(
                        RoundedRectangle(cornerRadius: AD.radius)
                            .stroke(Color.driveBaiPrimary, lineWidth: 1.5)
                    )
            }

            Button {
                Task {
                    if isLast { await vm.submit() }
                    else      { await vm.goForward() }
                }
            } label: {
                HStack(spacing: 8) {
                    if busy { ProgressView().tint(.white).scaleEffect(0.8) }
                    Text(isLast
                         ? (vm.isSubmitting ? "Submitting…" : "Submit Report")
                         : (vm.isSaving     ? "Saving…"    : "Next"))
                        .font(.subheadline.weight(.semibold))
                }
                .frame(maxWidth: .infinity, minHeight: AD.ctaHeight)
                .background(busy ? Color.driveBaiPrimary.opacity(0.6) : Color.driveBaiPrimary)
                .foregroundColor(.white)
                .cornerRadius(AD.radius)
            }
            .disabled(busy)
        }
        .padding(.horizontal, AD.cardPad)
        .padding(.top, 12)
        .padding(.bottom, 8)
        .background(
            Color(.systemBackground)
                .shadow(color: .black.opacity(0.07), radius: 10, x: 0, y: -4)
                .ignoresSafeArea(edges: .bottom)
        )
    }

    // MARK: Submitted view

    private var submittedView: some View {
        VStack(spacing: 28) {
            Spacer()
            ZStack {
                Circle()
                    .fill(Color.driveBaiPrimary.opacity(0.12))
                    .frame(width: 130, height: 130)
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 68))
                    .foregroundColor(.driveBaiPrimary)
            }
            VStack(spacing: 10) {
                Text("Report Submitted")
                    .font(.title2.bold())
                Text("Your accident report has been submitted.\nOur team will review it shortly.")
                    .multilineTextAlignment(.center)
                    .foregroundColor(.secondary)
                    .font(.subheadline)
                    .padding(.horizontal, 20)
            }
            Button("Close") { dismiss() }
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.white)
                .frame(maxWidth: 220, minHeight: AD.ctaHeight)
                .background(Color.driveBaiPrimary)
                .cornerRadius(AD.radius)
            Spacer()
        }
    }
}

// MARK: – Photos Step

private struct SlotConfig {
    let slot: String
    let label: String
    let subtitle: String
    let icon: String
    let isVideo: Bool
}

private struct PhotosStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    private let slots: [SlotConfig] = [
        SlotConfig(slot: "accident_photo",      label: "Accident Photos",
                   subtitle: "Photos of vehicles and the scene",
                   icon: "camera.fill",         isVideo: false),
        SlotConfig(slot: "accident_video",      label: "Accident Video",
                   subtitle: "Dashcam or bystander footage",
                   icon: "video.fill",          isVideo: true),
        SlotConfig(slot: "driver1_license",     label: "Driver 1 License",
                   subtitle: "Front of the driver's licence",
                   icon: "creditcard.fill",     isVideo: false),
        SlotConfig(slot: "driver2_plate",       label: "Second Vehicle Plate",
                   subtitle: "Clear photo of the plate number",
                   icon: "car.rear.fill",       isVideo: false),
        SlotConfig(slot: "second_vehicle_docs", label: "Second Vehicle Documents",
                   subtitle: "Registration or insurance card",
                   icon: "doc.text.fill",       isVideo: false),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Evidence & Documentation").font(.headline)
                Text("Upload photos, videos, and documents related to the accident.")
                    .font(.subheadline).foregroundColor(.secondary)
            }

            if vm.isUploadingAttachment {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("Uploading…").font(.subheadline).foregroundColor(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(Color.driveBaiPrimary.opacity(0.08))
                .cornerRadius(10)
            }

            ForEach(slots, id: \.slot) { config in
                SlotRowCard(config: config, vm: vm)
            }
        }
    }
}

private struct SlotRowCard: View {
    let config: SlotConfig
    @ObservedObject var vm: AccidentReportViewModel

    private var attachments: [AccidentAttachmentAPI] {
        vm.accident?.attachments.filter { $0.slot == config.slot } ?? []
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header row
            HStack(spacing: 12) {
                Image(systemName: config.icon)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 34, height: 34)
                    .background(Color.driveBaiPrimary.opacity(0.10))
                    .cornerRadius(8)

                VStack(alignment: .leading, spacing: 2) {
                    Text(config.label).font(.subheadline.weight(.semibold))
                    Text(attachments.isEmpty
                         ? config.subtitle
                         : "\(attachments.count) file\(attachments.count == 1 ? "" : "s") added")
                        .font(.caption)
                        .foregroundColor(attachments.isEmpty ? .secondary : .driveBaiPrimary)
                }

                Spacer()

                SlotAddButton(config: config, vm: vm)
            }

            // Content area
            if attachments.isEmpty {
                HStack(spacing: 8) {
                    Image(systemName: config.isVideo ? "video.badge.plus" : "photo.badge.plus")
                        .font(.title2)
                        .foregroundColor(Color(.systemGray3))
                    Text("Tap \"Add\" to upload a file")
                        .font(.caption)
                        .foregroundColor(Color(.systemGray3))
                }
                .frame(maxWidth: .infinity, alignment: .center)
                .frame(height: 52)
                .background(Color(.systemGray6))
                .cornerRadius(10)
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 10) {
                        ForEach(attachments) { att in
                            AttachmentThumbnail(att: att, vm: vm)
                        }
                    }
                    .padding(.vertical, 2)
                    .padding(.horizontal, 1)
                }
            }
        }
        .padding(AD.cardPad)
        .background(Color(.systemBackground))
        .cornerRadius(AD.radius)
        .shadow(color: .black.opacity(0.05), radius: 8, x: 0, y: 2)
    }
}

private struct SlotAddButton: View {
    let config: SlotConfig
    @ObservedObject var vm: AccidentReportViewModel
    @State private var selectedItem: PhotosPickerItem?

    var body: some View {
        PhotosPicker(
            selection: $selectedItem,
            matching: config.isVideo ? .videos : .images
        ) {
            HStack(spacing: 4) {
                Image(systemName: "plus").font(.caption.weight(.bold))
                Text("Add").font(.caption.weight(.semibold))
            }
            .foregroundColor(.driveBaiPrimary)
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .background(Color.driveBaiPrimary.opacity(0.10))
            .cornerRadius(20)
        }
        .onChange(of: selectedItem) { _, item in
            guard let item else { return }
            Task {
                guard let data = try? await item.loadTransferable(type: Data.self) else { return }
                let filename = config.isVideo ? "video.mov" : "photo.jpg"
                let mime     = config.isVideo ? "video/quicktime" : "image/jpeg"
                await vm.uploadAttachment(slot: config.slot, data: data, filename: filename, mimeType: mime)
                selectedItem = nil
            }
        }
    }
}

private struct AttachmentThumbnail: View {
    let att: AccidentAttachmentAPI
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        ZStack(alignment: .topTrailing) {
            thumbnailBody
            deleteButton
        }
    }

    @ViewBuilder
    private var thumbnailBody: some View {
        if att.mimeType.hasPrefix("image/"),
           let url = URL(string: AppConfig.serverBaseURL.absoluteString + att.fileUrl) {
            RemoteImage(url: url, contentMode: .fill, maxPixelSize: 300)
                .frame(width: AD.thumbSize, height: AD.thumbSize)
                .clipped()
                .cornerRadius(10)
        } else if att.mimeType.hasPrefix("image/") {
            Color(.systemGray5)
                .overlay(Image(systemName: "photo").foregroundColor(.secondary))
                .frame(width: AD.thumbSize, height: AD.thumbSize)
                .clipped()
                .cornerRadius(10)
        } else {
            VStack(spacing: 6) {
                Image(systemName: att.mimeType.contains("video") ? "play.rectangle.fill" : "doc.fill")
                    .font(.title2)
                    .foregroundColor(.driveBaiPrimary)
                Text(att.mimeType.contains("video") ? "Video" : "Doc")
                    .font(.caption2.weight(.medium))
                    .foregroundColor(.secondary)
            }
            .frame(width: AD.thumbSize, height: AD.thumbSize)
            .background(Color(.systemGray6))
            .cornerRadius(10)
        }
    }

    private var deleteButton: some View {
        Button {
            Task { await vm.deleteAttachment(id: att.id) }
        } label: {
            ZStack {
                Circle()
                    .fill(Color(.systemBackground))
                    .frame(width: 24, height: 24)
                Image(systemName: "xmark.circle.fill")
                    .font(.system(size: 24))
                    .foregroundColor(Color(.systemGray2))
            }
        }
        .offset(x: 5, y: -5)
    }
}

// MARK: – Driver Info Step

struct DriverInfoStepView: View {
    @Binding var info: DriverInfoAPI
    let label: String

    var body: some View {
        VStack(alignment: .leading, spacing: AD.sectionGap) {
            SectionCard(title: "\(label) — Driver Info") {
                VStack(spacing: AD.fieldGap) {
                    FormField("Driver License ID", text: $info.driverLicenseId)
                    FormField("State of License", text: $info.stateOfLicense)
                    FormField("Driver Name (Last, First, M.I.)", text: $info.driverName)
                    FormField("Address", text: $info.address)
                    HStack(spacing: 8) {
                        FormField("City", text: $info.city)
                        FormField("St.", text: $info.state).frame(maxWidth: 60)
                        FormField("ZIP", text: $info.zip).frame(maxWidth: 84)
                    }
                    FormField("Date of Birth (mm.dd.yyyy)", text: $info.dob)
                    FormField("People in Vehicle", text: $info.peopleInVehicle)
                        .keyboardType(.numberPad)
                    FormPicker("Public Property Damaged?",
                               selection: $info.publicPropertyDamaged,
                               options: ["", "Yes", "No"])
                    FormField("Injuries", text: $info.injuries, axis: .vertical)
                }
            }

            SectionCard(title: "\(label) — Registrant") {
                VStack(spacing: AD.fieldGap) {
                    FormField("Name (as on registration)", text: $info.registrantName)
                    FormField("Address", text: $info.registrantAddress)
                    HStack(spacing: 8) {
                        FormField("City", text: $info.registrantCity)
                        FormField("St.", text: $info.registrantState).frame(maxWidth: 60)
                        FormField("ZIP", text: $info.registrantZip).frame(maxWidth: 84)
                    }
                    HStack(spacing: 8) {
                        FormField("Plate Number", text: $info.plateNumber)
                        FormField("State of Reg.", text: $info.stateOfReg).frame(maxWidth: 110)
                    }
                    FormField("Vehicle Year & Make", text: $info.vehicleYearMake)
                    FormField("Vehicle Type", text: $info.vehicleType)
                    FormField("Insurance Code", text: $info.insCode)
                }
            }
        }
    }
}

private struct Driver2StepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: AD.sectionGap) {
            // Toggle card
            Toggle(isOn: $vm.hasSecondDriver) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Second driver involved?").font(.subheadline.weight(.semibold))
                    Text("Enable if another vehicle was in the accident")
                        .font(.caption).foregroundColor(.secondary)
                }
            }
            .tint(.driveBaiPrimary)
            .padding(AD.cardPad)
            .background(Color(.systemBackground))
            .cornerRadius(AD.radius)
            .shadow(color: .black.opacity(0.05), radius: 6, x: 0, y: 2)

            if vm.hasSecondDriver {
                DriverInfoStepView(info: $vm.driver2Info, label: "Driver 2")
            } else {
                HStack(spacing: 14) {
                    Image(systemName: "person.badge.minus")
                        .font(.title2).foregroundColor(Color(.systemGray3))
                    Text("No second driver — skip to the next step.")
                        .font(.subheadline).foregroundColor(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(AD.cardPad)
                .background(Color(.systemBackground))
                .cornerRadius(AD.radius)
            }
        }
    }
}

// MARK: – Accident Diagram Model

private struct AccidentDiagramOption {
    let id: Int
    let title: String
    let imageName: String
    let accessibilityLabel: String

    static let all: [AccidentDiagramOption] = [
        AccidentDiagramOption(id: 0, title: "Left Turn",         imageName: "diagram_0_left_turn",         accessibilityLabel: "Diagram 0, Left turn collision"),
        AccidentDiagramOption(id: 1, title: "Rear End",          imageName: "diagram_1_rear_end",          accessibilityLabel: "Diagram 1, Rear end collision"),
        AccidentDiagramOption(id: 2, title: "Sideswipe (same)",  imageName: "diagram_2_sideswipe_same",    accessibilityLabel: "Diagram 2, Sideswipe same direction"),
        AccidentDiagramOption(id: 3, title: "Left Turn ↓",       imageName: "diagram_3_left_turn_crossing",accessibilityLabel: "Diagram 3, Left turn crossing"),
        AccidentDiagramOption(id: 4, title: "Right Angle",       imageName: "diagram_4_right_angle",       accessibilityLabel: "Diagram 4, Right angle T-bone"),
        AccidentDiagramOption(id: 5, title: "Right Turn",        imageName: "diagram_5_right_turn",        accessibilityLabel: "Diagram 5, Right turn collision"),
        AccidentDiagramOption(id: 6, title: "Right Turn ↓",      imageName: "diagram_6_right_turn_merge",  accessibilityLabel: "Diagram 6, Right turn merge"),
        AccidentDiagramOption(id: 7, title: "Head On",           imageName: "diagram_7_head_on",           accessibilityLabel: "Diagram 7, Head on collision"),
        AccidentDiagramOption(id: 8, title: "Sideswipe (opp.)",  imageName: "diagram_8_sideswipe_opposite",accessibilityLabel: "Diagram 8, Sideswipe opposite direction"),
    ]
}

// MARK: – Diagram Card
// 2-column layout gives each tile ~175pt width on standard iPhones, enough for
// the collision diagrams to be legible without making the card excessively tall.

private struct DiagramCard: View {
    let option: AccidentDiagramOption
    let isSelected: Bool
    let onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            VStack(spacing: 0) {
                Image(option.imageName)
                    .resizable()
                    .scaledToFit()
                    .frame(height: 120)
                    .padding(8)
                    .frame(maxWidth: .infinity)
                    .background(Color(.systemBackground))

                // Caption strip: teal when selected, muted gray otherwise.
                Text(option.title)
                    .font(.caption.weight(.semibold))
                    .foregroundColor(isSelected ? .white : .secondary)
                    .multilineTextAlignment(.center)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 9)
                    .background(isSelected ? Color.driveBaiPrimary : Color(.systemGray5))
            }
            .clipShape(RoundedRectangle(cornerRadius: AD.radius))
            .overlay(
                RoundedRectangle(cornerRadius: AD.radius)
                    .stroke(
                        isSelected ? Color.driveBaiPrimary : Color(.systemGray4),
                        lineWidth: isSelected ? 2.5 : 1
                    )
            )
            .shadow(
                color: isSelected ? Color.driveBaiPrimary.opacity(0.22) : .black.opacity(0.05),
                radius: isSelected ? 8 : 3, x: 0, y: 2
            )
        }
        .buttonStyle(.plain)
        .animation(.easeInOut(duration: 0.15), value: isSelected)
        .accessibilityLabel("Accident diagram: \(option.title)")
        .accessibilityAddTraits(.isButton)
        .accessibilityValue(isSelected ? "Selected" : "")
    }
}

// MARK: – Damage Step

private struct DamageStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: AD.sectionGap) {
            SectionCard(title: "Vehicle Damage Description") {
                ZStack(alignment: .topLeading) {
                    TextEditor(text: $vm.vehicleDamage.description)
                        .font(.subheadline)
                        .frame(minHeight: 110)
                        .padding(10)
                        .background(Color(.systemGray6))
                        .cornerRadius(10)
                    if vm.vehicleDamage.description.isEmpty {
                        Text("Describe the damage to vehicle 1…")
                            .font(.subheadline)
                            .foregroundColor(Color(.systemGray3))
                            .padding(14)
                            .allowsHitTesting(false)
                    }
                }
            }

            SectionCard(title: "Accident Diagram") {
                LazyVGrid(
                    columns: Array(repeating: .init(.flexible(), spacing: 10), count: 2),
                    spacing: 10
                ) {
                    ForEach(AccidentDiagramOption.all, id: \.id) { option in
                        DiagramCard(
                            option: option,
                            isSelected: vm.vehicleDamage.diagram == option.id,
                            onTap: { vm.vehicleDamage.diagram = option.id }
                        )
                    }
                }
            }
        }
    }
}

// MARK: – Description Step

private struct DescriptionStepView: View {
    @Binding var text: String

    var body: some View {
        SectionCard(title: "How did the accident happen?") {
            ZStack(alignment: .topLeading) {
                TextEditor(text: $text)
                    .font(.subheadline)
                    .frame(minHeight: 200)
                    .padding(10)
                    .background(Color(.systemGray6))
                    .cornerRadius(10)
                if text.isEmpty {
                    Text("Describe the sequence of events leading to the collision…")
                        .font(.subheadline)
                        .foregroundColor(Color(.systemGray3))
                        .padding(14)
                        .allowsHitTesting(false)
                }
            }
        }
    }
}

// MARK: – Insurance Step

private struct InsuranceStepView: View {
    @Binding var info: InsuranceInfoAPI

    var body: some View {
        SectionCard(title: "Insurance — Vehicle 1") {
            VStack(spacing: AD.fieldGap) {
                FormField("Insurance Company Name", text: $info.insuranceCompany)
                FormField("VIN", text: $info.vin)
                FormField("Policy Number", text: $info.policyNumber)
                HStack(spacing: 8) {
                    FormField("Period From", text: $info.policyPeriodFrom)
                    FormField("Period To",   text: $info.policyPeriodTo)
                }
            }
        }
    }
}

// MARK: – Other Info Step

private struct OtherInfoStepView: View {
    @Binding var info: OtherInfoAPI

    var body: some View {
        SectionCard(title: "Accident Details") {
            VStack(spacing: AD.fieldGap) {
                HStack(spacing: 8) {
                    FormField("Month", text: $info.month).keyboardType(.numberPad)
                    FormField("Day",   text: $info.day).keyboardType(.numberPad)
                    FormField("Year",  text: $info.year).keyboardType(.numberPad)
                }
                FormField("Day of Week", text: $info.dayOfWeek)
                FormField("Time (e.g. 02:30 PM)", text: $info.time)
                HStack(spacing: 8) {
                    FormField("Vehicles", text: $info.numVehicles).keyboardType(.numberPad)
                    FormField("Injured",  text: $info.numInjured).keyboardType(.numberPad)
                    FormField("Killed",   text: $info.numKilled).keyboardType(.numberPad)
                }
                FormPicker("Police Investigated?",
                           selection: $info.policeInvestigated,
                           options: ["", "Yes", "No", "Unknown"])
            }
        }
    }
}

// MARK: – Signature Step

private struct SignatureStepView: View {
    @ObservedObject var vm: AccidentReportViewModel
    @State private var lines: [[CGPoint]] = []
    @State private var currentLine: [CGPoint] = []

    private var isEmpty: Bool { lines.isEmpty && currentLine.isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: AD.sectionGap) {
            Text("Draw your signature below with your finger.")
                .font(.subheadline).foregroundColor(.secondary)

            // Canvas card
            ZStack {
                // Outer card background
                RoundedRectangle(cornerRadius: AD.radius + 2)
                    .fill(Color(.systemGray6))
                    .padding(4)

                // Drawing surface
                RoundedRectangle(cornerRadius: AD.radius)
                    .fill(Color(.systemBackground))
                    .overlay(
                        RoundedRectangle(cornerRadius: AD.radius)
                            .stroke(isEmpty ? Color(.systemGray4) : Color.driveBaiPrimary.opacity(0.5),
                                    lineWidth: isEmpty ? 1 : 1.5)
                    )

                // Ink
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
                        .onEnded   { _   in lines.append(currentLine); currentLine = [] }
                )
                .cornerRadius(AD.radius)

                // Placeholder
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

            // Action buttons — same height
            HStack(spacing: 12) {
                Button {
                    lines = []
                    currentLine = []
                    vm.signatureImageData = nil
                } label: {
                    Text("Clear")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity, minHeight: AD.ctaHeight)
                        .foregroundColor(isEmpty ? Color(.systemGray3) : .driveBaiPrimary)
                        .overlay(
                            RoundedRectangle(cornerRadius: AD.radius)
                                .stroke(isEmpty ? Color(.systemGray4) : Color.driveBaiPrimary, lineWidth: 1.5)
                        )
                }
                .disabled(isEmpty)

                Button {
                    if let data = renderSignature(lines: lines, size: CGSize(width: 340, height: 220)) {
                        Task { await vm.uploadSignature(imageData: data) }
                    }
                } label: {
                    HStack(spacing: 6) {
                        if vm.isSaving { ProgressView().tint(.white).scaleEffect(0.8) }
                        Text(vm.isSaving ? "Saving…" : "Save Signature")
                            .font(.subheadline.weight(.semibold))
                    }
                    .frame(maxWidth: .infinity, minHeight: AD.ctaHeight)
                    .background((isEmpty || vm.isSaving) ? Color(.systemGray4) : Color.driveBaiPrimary)
                    .foregroundColor(.white)
                    .cornerRadius(AD.radius)
                }
                .disabled(isEmpty || vm.isSaving)
            }

            // Confirmation banner
            if vm.accident?.signatureUrl != nil {
                Label("Signature saved", systemImage: "checkmark.seal.fill")
                    .font(.subheadline.weight(.medium))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(12)
                    .background(Color.driveBaiPrimary.opacity(0.08))
                    .cornerRadius(AD.radius)
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

// MARK: – Review Step

private struct ReviewStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: AD.fieldGap) {
            Text("Review your report before submitting.")
                .font(.subheadline).foregroundColor(.secondary)

            VStack(spacing: 0) {
                AccidentReviewRow(icon: "paperclip",     label: "Files",
                                  value: "\(vm.accident?.attachments.count ?? 0) uploaded")
                AccidentReviewRow(icon: "person.fill",   label: "Driver 1 Name",
                                  value: vm.driver1Info.driverName.isEmpty
                                    ? "Not filled" : vm.driver1Info.driverName)
                AccidentReviewRow(icon: "person.2.fill", label: "Second Driver",
                                  value: vm.hasSecondDriver ? "Yes" : "No")
                AccidentReviewRow(icon: "car.side.fill", label: "Damage Diagram",
                                  value: "Diagram \(vm.vehicleDamage.diagram) — \(AccidentDiagramOption.all[vm.vehicleDamage.diagram].title)")
                AccidentReviewRow(icon: "text.quote",    label: "Description",
                                  value: vm.accidentDescription.isEmpty
                                    ? "Not provided"
                                    : String(vm.accidentDescription.prefix(70)) + (vm.accidentDescription.count > 70 ? "…" : ""))
                AccidentReviewRow(icon: "pencil.line",   label: "Signature",
                                  value: vm.accident?.signatureUrl != nil ? "Signed ✓" : "Not signed",
                                  isLast: true)
            }
            .background(Color(.systemBackground))
            .cornerRadius(AD.radius)
            .shadow(color: .black.opacity(0.05), radius: 6, x: 0, y: 2)

            Text("By submitting, you confirm this report is accurate to the best of your knowledge.")
                .font(.caption).foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.top, 4)
        }
    }
}

private struct AccidentReviewRow: View {
    let icon: String
    let label: String
    let value: String
    var isLast: Bool = false

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Image(systemName: icon)
                    .font(.system(size: 14, weight: .medium))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 22, alignment: .center)
                Text(label)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .frame(minWidth: 110, alignment: .leading)
                Spacer()
                Text(value)
                    .font(.subheadline.weight(.medium))
                    .foregroundColor(.primary)
                    .multilineTextAlignment(.trailing)
            }
            .padding(.horizontal, AD.cardPad)
            .padding(.vertical, 13)

            if !isLast {
                Divider().padding(.leading, 48)
            }
        }
    }
}

// MARK: – Reusable Form Components

struct FormField: View {
    let label: String
    @Binding var text: String
    var axis: Axis = .horizontal

    init(_ label: String, text: Binding<String>, axis: Axis = .horizontal) {
        self.label = label
        self._text = text
        self.axis = axis
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundColor(.secondary)
            TextField(label, text: $text, axis: axis)
                .font(.subheadline)
                .lineLimit(axis == .vertical ? 3...6 : 1...1)
                .padding(.horizontal, 11)
                .padding(.vertical, 9)
                .background(Color(.systemGray6))
                .cornerRadius(9)
        }
    }
}

struct FormPicker: View {
    let label: String
    @Binding var selection: String
    let options: [String]

    init(_ label: String, selection: Binding<String>, options: [String]) {
        self.label = label
        self._selection = selection
        self.options = options
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundColor(.secondary)
            Picker(label, selection: $selection) {
                ForEach(options, id: \.self) { opt in
                    Text(opt.isEmpty ? "Select…" : opt).tag(opt)
                }
            }
            .pickerStyle(.menu)
            .font(.subheadline)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 8)
            .padding(.vertical, 6)
            .background(Color(.systemGray6))
            .cornerRadius(9)
        }
    }
}

// MARK: – Section card

private struct SectionCard<Content: View>: View {
    let title: String
    let content: Content

    init(title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.primary)
            content
        }
        .padding(AD.cardPad)
        .background(Color(.systemBackground))
        .cornerRadius(AD.radius)
        .shadow(color: .black.opacity(0.05), radius: 6, x: 0, y: 2)
    }
}
