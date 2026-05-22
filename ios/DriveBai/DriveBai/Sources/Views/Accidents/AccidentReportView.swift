import SwiftUI
import PhotosUI

struct AccidentReportView: View {
    @StateObject private var vm: AccidentReportViewModel
    @Environment(\.dismiss) private var dismiss

    init(relatedChatId: UUID? = nil, relatedCarId: UUID? = nil) {
        _vm = StateObject(wrappedValue: AccidentReportViewModel(
            relatedChatId: relatedChatId, relatedCarId: relatedCarId
        ))
    }

    var body: some View {
        NavigationStack {
            Group {
                if vm.isLoading {
                    ProgressView("Creating report…")
                } else if vm.isSubmitted {
                    submittedView
                } else {
                    stepView
                }
            }
            .navigationTitle(vm.currentStep.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    if vm.isSaving { ProgressView().scaleEffect(0.7) }
                }
            }
            .alert("Error", isPresented: Binding(
                get: { vm.error != nil },
                set: { if !$0 { vm.error = nil } }
            )) {
                Button("OK", role: .cancel) {}
            } message: { Text(vm.error ?? "") }
        }
        .task { await vm.loadOrCreate() }
    }

    // MARK: - Step content

    @ViewBuilder
    private var stepView: some View {
        VStack(spacing: 0) {
            // Progress bar
            GeometryReader { geo in
                let total = AccidentStep.allCases.count
                let progress = Double(vm.currentStep.rawValue + 1) / Double(total)
                ZStack(alignment: .leading) {
                    Rectangle().fill(Color(.systemGray5)).frame(height: 3)
                    Rectangle().fill(Color.driveBaiPrimary).frame(width: geo.size.width * progress, height: 3)
                }
            }
            .frame(height: 3)

            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
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
                .padding()
            }

            // Nav buttons
            HStack(spacing: 12) {
                if vm.canGoBack {
                    Button("Back") { vm.goBack() }
                        .buttonStyle(.bordered)
                }
                Spacer()
                if vm.currentStep == .review {
                    Button(vm.isSubmitting ? "Submitting…" : "Submit Report") {
                        Task { await vm.submit() }
                    }
                    .buttonStyle(DriveBaiButtonStyle())
                    .disabled(vm.isSubmitting)
                } else {
                    Button(vm.isSaving ? "Saving…" : "Next") {
                        Task { await vm.goForward() }
                    }
                    .buttonStyle(DriveBaiButtonStyle())
                    .disabled(vm.isSaving)
                }
            }
            .padding()
            .background(Color(.systemBackground))
        }
    }

    private var submittedView: some View {
        VStack(spacing: 24) {
            Spacer()
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 72))
                .foregroundColor(.driveBaiPrimary)
            Text("Report Submitted")
                .font(.title2.bold())
            Text("Your accident report has been submitted. Our team will review it shortly.")
                .multilineTextAlignment(.center)
                .foregroundColor(.secondary)
                .padding(.horizontal, 32)
            Button("Close") { dismiss() }
                .buttonStyle(DriveBaiButtonStyle())
                .padding(.horizontal, 40)
            Spacer()
        }
    }
}

// MARK: - Photos Step

private struct PhotosStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    private let slots = [
        ("accident_photo", "Accident Photos"),
        ("accident_video", "Accident Video"),
        ("driver1_license", "Driver 1 License"),
        ("driver2_plate", "Plate of Second Vehicle"),
        ("second_vehicle_docs", "Second Vehicle Documents"),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Upload photos, videos, and documents related to the accident.")
                .font(.subheadline)
                .foregroundColor(.secondary)

            ForEach(slots, id: \.0) { slot, label in
                let attachments = vm.accident?.attachments.filter { $0.slot == slot } ?? []
                VStack(alignment: .leading, spacing: 8) {
                    Text(label).font(.subheadline.bold())
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 8) {
                            ForEach(attachments) { att in
                                if att.mimeType.hasPrefix("image/") {
                                    AsyncImage(url: URL(string: AppConfig.serverBaseURL.absoluteString + att.fileUrl)) { phase in
                                        switch phase {
                                        case .success(let img):
                                            img.resizable().scaledToFill()
                                                .frame(width: 80, height: 80).clipped()
                                                .cornerRadius(8)
                                        default:
                                            Color(.systemGray5).frame(width: 80, height: 80).cornerRadius(8)
                                        }
                                    }
                                    .overlay(alignment: .topTrailing) {
                                        Button {
                                            Task { await vm.deleteAttachment(id: att.id) }
                                        } label: {
                                            Image(systemName: "xmark.circle.fill")
                                                .foregroundColor(.red)
                                                .background(Color.white.clipShape(Circle()))
                                        }
                                        .padding(4)
                                    }
                                } else {
                                    HStack {
                                        Image(systemName: "doc.fill").foregroundColor(.driveBaiPrimary)
                                        Text(att.mimeType.contains("video") ? "Video" : "Doc")
                                            .font(.caption)
                                    }
                                    .padding(8)
                                    .background(Color(.systemGray6))
                                    .cornerRadius(8)
                                }
                            }
                            // Add button
                            PhotoPickerButton(slot: slot, vm: vm)
                        }
                        .padding(.horizontal, 2)
                    }
                }
                Divider()
            }

            if vm.isUploadingAttachment {
                HStack {
                    ProgressView()
                    Text("Uploading…").font(.subheadline).foregroundColor(.secondary)
                }
            }
        }
    }
}

// MARK: - Photo Picker Button

private struct PhotoPickerButton: View {
    let slot: String
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        ZStack {
            Image(systemName: "plus")
                .font(.title2)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 80, height: 80)
                .background(Color(.systemGray6))
                .cornerRadius(8)
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.driveBaiPrimary.opacity(0.4), lineWidth: 1))
            PhotoPickerOverlay(slot: slot, vm: vm)
        }
    }
}

private struct PhotoPickerOverlay: View {
    let slot: String
    @ObservedObject var vm: AccidentReportViewModel
    @State private var selectedItem: PhotosPickerItem?

    var body: some View {
        PhotosPicker(
            selection: $selectedItem,
            matching: slot == "accident_video" ? .videos : .images
        ) {
            Color.clear
        }
        .onChange(of: selectedItem) { _, item in
            guard let item else { return }
            Task {
                guard let data = try? await item.loadTransferable(type: Data.self) else { return }
                let isVideo = slot == "accident_video"
                let filename = isVideo ? "video.mov" : "photo.jpg"
                let mime = isVideo ? "video/quicktime" : "image/jpeg"
                await vm.uploadAttachment(slot: slot, data: data, filename: filename, mimeType: mime)
                selectedItem = nil
            }
        }
    }
}

// MARK: - Driver Info Step

struct DriverInfoStepView: View {
    @Binding var info: DriverInfoAPI
    let label: String

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("\(label) — Driver Info").font(.headline)
            Group {
                FormField("Driver License ID Number", text: $info.driverLicenseId)
                FormField("State of License", text: $info.stateOfLicense)
                FormField("Driver Name (Last, First, M.I.)", text: $info.driverName)
                FormField("Address", text: $info.address)
                HStack(spacing: 8) {
                    FormField("City", text: $info.city)
                    FormField("State", text: $info.state)
                    FormField("ZIP", text: $info.zip)
                }
                FormField("Date of Birth (mm.dd.yyyy)", text: $info.dob)
                FormField("Number of People in Vehicle", text: $info.peopleInVehicle)
                    .keyboardType(.numberPad)
                FormPicker("Public Property Damaged", selection: $info.publicPropertyDamaged,
                           options: ["", "Yes", "No"])
                FormField("Injuries", text: $info.injuries, axis: .vertical)
            }
            Text("\(label) — Registrant").font(.headline).padding(.top, 8)
            Group {
                FormField("Name (as on registration)", text: $info.registrantName)
                FormField("Address", text: $info.registrantAddress)
                HStack(spacing: 8) {
                    FormField("City", text: $info.registrantCity)
                    FormField("State", text: $info.registrantState)
                    FormField("ZIP", text: $info.registrantZip)
                }
                HStack(spacing: 8) {
                    FormField("Plate Number", text: $info.plateNumber)
                    FormField("State of Reg.", text: $info.stateOfReg)
                }
                FormField("Vehicle Year & Make", text: $info.vehicleYearMake)
                FormField("Vehicle Type", text: $info.vehicleType)
                FormField("Ins. Code", text: $info.insCode)
            }
        }
    }
}

private struct Driver2StepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Toggle("Second driver involved?", isOn: $vm.hasSecondDriver)
                .font(.subheadline.bold())
                .tint(.driveBaiPrimary)
            if vm.hasSecondDriver {
                DriverInfoStepView(info: $vm.driver2Info, label: "Driver 2")
            } else {
                Text("Skip this step if there was no second driver.")
                    .foregroundColor(.secondary)
                    .font(.subheadline)
            }
        }
    }
}

// MARK: - Damage Step

private struct DamageStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    private let diagrams = [
        (0, "Left Turn"), (1, "Rear End"), (2, "Sideswipe (same)"),
        (3, "Left Turn ↓"), (4, "Right Angle"), (5, "Right Turn"),
        (6, "Right Turn ↓"), (7, "Head On"), (8, "Sideswipe (opp.)"),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Describe damage to vehicle 1")
                .font(.subheadline.bold())
            TextEditor(text: $vm.vehicleDamage.description)
                .frame(minHeight: 100)
                .padding(8)
                .background(Color(.systemGray6))
                .cornerRadius(10)

            Text("Accident Diagram (0–8)")
                .font(.subheadline.bold())
            LazyVGrid(columns: Array(repeating: .init(.flexible()), count: 3), spacing: 10) {
                ForEach(diagrams, id: \.0) { num, label in
                    Button {
                        vm.vehicleDamage.diagram = num
                    } label: {
                        VStack(spacing: 4) {
                            Text("\(num)")
                                .font(.title3.bold())
                                .foregroundColor(vm.vehicleDamage.diagram == num ? .white : .primary)
                            Text(label)
                                .font(.caption2)
                                .foregroundColor(vm.vehicleDamage.diagram == num ? .white.opacity(0.85) : .secondary)
                                .multilineTextAlignment(.center)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(vm.vehicleDamage.diagram == num ? Color.driveBaiPrimary : Color(.systemGray6))
                        .cornerRadius(10)
                    }
                }
            }
        }
    }
}

// MARK: - Description Step

private struct DescriptionStepView: View {
    @Binding var text: String
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("How did the accident happen?").font(.subheadline.bold())
            TextEditor(text: $text)
                .frame(minHeight: 180)
                .padding(8)
                .background(Color(.systemGray6))
                .cornerRadius(10)
        }
    }
}

// MARK: - Insurance Step

private struct InsuranceStepView: View {
    @Binding var info: InsuranceInfoAPI
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Insurance — Vehicle 1").font(.headline)
            FormField("Insurance Company Name", text: $info.insuranceCompany)
            FormField("VIN", text: $info.vin)
            FormField("Policy Number", text: $info.policyNumber)
            HStack(spacing: 8) {
                FormField("Period From", text: $info.policyPeriodFrom)
                FormField("Period To", text: $info.policyPeriodTo)
            }
        }
    }
}

// MARK: - Other Info Step

private struct OtherInfoStepView: View {
    @Binding var info: OtherInfoAPI
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Accident Details").font(.headline)
            HStack(spacing: 8) {
                FormField("Month", text: $info.month).keyboardType(.numberPad)
                FormField("Day", text: $info.day).keyboardType(.numberPad)
                FormField("Year", text: $info.year).keyboardType(.numberPad)
            }
            FormField("Day of Week", text: $info.dayOfWeek)
            FormField("Time (HH:MM AM/PM)", text: $info.time)
            FormField("Number of Vehicles", text: $info.numVehicles).keyboardType(.numberPad)
            FormField("Number Injured", text: $info.numInjured).keyboardType(.numberPad)
            FormField("Number Killed", text: $info.numKilled).keyboardType(.numberPad)
            FormPicker("Police Investigated?", selection: $info.policeInvestigated,
                       options: ["", "Yes", "No", "Unknown"])
        }
    }
}

// MARK: - Signature Step

private struct SignatureStepView: View {
    @ObservedObject var vm: AccidentReportViewModel
    @State private var lines: [[CGPoint]] = []
    @State private var currentLine: [CGPoint] = []

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Draw your signature below with your finger.")
                .font(.subheadline)
                .foregroundColor(.secondary)

            ZStack {
                RoundedRectangle(cornerRadius: 12)
                    .fill(Color(.systemBackground))
                    .frame(height: 200)
                    .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color(.systemGray4), lineWidth: 1))

                Canvas { ctx, _ in
                    for line in lines + [currentLine] {
                        guard line.count > 1 else { continue }
                        var path = Path()
                        path.move(to: line[0])
                        for pt in line.dropFirst() { path.addLine(to: pt) }
                        ctx.stroke(path, with: .color(.primary), style: StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
                    }
                }
                .gesture(
                    DragGesture(minimumDistance: 0)
                        .onChanged { val in
                            currentLine.append(val.location)
                        }
                        .onEnded { _ in
                            lines.append(currentLine)
                            currentLine = []
                        }
                )

                if lines.isEmpty && currentLine.isEmpty {
                    Text("Sign here").foregroundColor(Color(.systemGray4)).font(.body)
                }
            }

            HStack {
                Button("Clear") {
                    lines = []
                    currentLine = []
                    vm.signatureImageData = nil
                }
                .buttonStyle(.bordered)

                Spacer()

                Button(vm.isSaving ? "Saving…" : "Save Signature") {
                    let imgData = renderSignature(lines: lines, size: CGSize(width: 340, height: 200))
                    if let data = imgData {
                        Task { await vm.uploadSignature(imageData: data) }
                    }
                }
                .buttonStyle(DriveBaiButtonStyle())
                .disabled(lines.isEmpty || vm.isSaving)
            }

            if vm.accident?.signatureUrl != nil {
                Label("Signature saved", systemImage: "checkmark.circle.fill")
                    .foregroundColor(.green)
                    .font(.subheadline)
            }
        }
    }

    private func renderSignature(lines: [[CGPoint]], size: CGSize) -> Data? {
        let renderer = UIGraphicsImageRenderer(size: size)
        let uiImage = renderer.image { ctx in
            UIColor.white.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
            UIColor.black.setStroke()
            ctx.cgContext.setLineWidth(2)
            ctx.cgContext.setLineCap(.round)
            ctx.cgContext.setLineJoin(.round)
            for line in lines {
                guard line.count > 1 else { continue }
                ctx.cgContext.beginPath()
                ctx.cgContext.move(to: line[0])
                for pt in line.dropFirst() { ctx.cgContext.addLine(to: pt) }
                ctx.cgContext.strokePath()
            }
        }
        return uiImage.pngData()
    }
}

// MARK: - Review Step

private struct ReviewStepView: View {
    @ObservedObject var vm: AccidentReportViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Review your report before submitting.")
                .font(.subheadline).foregroundColor(.secondary)

            AccidentReviewRow(label:"Attachments", value: "\(vm.accident?.attachments.count ?? 0) file(s) uploaded")
            AccidentReviewRow(label:"Driver 1 Name", value: vm.driver1Info.driverName.isEmpty ? "Not filled" : vm.driver1Info.driverName)
            AccidentReviewRow(label:"Second Driver", value: vm.hasSecondDriver ? "Yes" : "No")
            AccidentReviewRow(label:"Damage Diagram", value: "Diagram \(vm.vehicleDamage.diagram)")
            AccidentReviewRow(label:"Description", value: vm.accidentDescription.isEmpty ? "Not provided" : String(vm.accidentDescription.prefix(60)) + (vm.accidentDescription.count > 60 ? "…" : ""))
            AccidentReviewRow(label:"Signature", value: vm.accident?.signatureUrl != nil ? "Signed ✓" : "Not signed")

            Text("By submitting, you confirm this report is accurate to the best of your knowledge.")
                .font(.caption)
                .foregroundColor(.secondary)
                .padding(.top, 8)
        }
    }
}

private struct AccidentReviewRow: View {
    let label: String
    let value: String
    var body: some View {
        HStack(alignment: .top) {
            Text(label).font(.subheadline).foregroundColor(.secondary).frame(width: 130, alignment: .leading)
            Text(value).font(.subheadline).foregroundColor(.primary)
            Spacer()
        }
        .padding(.vertical, 4)
        Divider()
    }
}

// MARK: - Reusable form components

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
            Text(label).font(.caption).foregroundColor(.secondary)
            TextField(label, text: $text, axis: axis)
                .lineLimit(axis == .vertical ? 3...6 : 1...1)
                .padding(10)
                .background(Color(.systemGray6))
                .cornerRadius(8)
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
            Text(label).font(.caption).foregroundColor(.secondary)
            Picker(label, selection: $selection) {
                ForEach(options, id: \.self) { opt in
                    Text(opt.isEmpty ? "Choose an option" : opt).tag(opt)
                }
            }
            .pickerStyle(.menu)
            .padding(8)
            .background(Color(.systemGray6))
            .cornerRadius(8)
        }
    }
}
