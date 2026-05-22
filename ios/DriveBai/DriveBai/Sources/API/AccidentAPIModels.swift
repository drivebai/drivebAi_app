import Foundation

// MARK: - Accident API Models

struct AccidentAPIResponse: Codable, Identifiable {
    let id: UUID
    let reporterId: UUID
    let relatedChatId: UUID?
    let relatedCarId: UUID?
    let status: String
    let driver1Info: DriverInfoAPI?
    let driver2Info: DriverInfoAPI?
    let vehicleDamage: VehicleDamageAPI?
    let accidentDescription: String?
    let insuranceInfo: InsuranceInfoAPI?
    let otherInfo: OtherInfoAPI?
    let signatureUrl: String?
    let signatureSignedAt: Date?
    let submittedAt: Date?
    let attachments: [AccidentAttachmentAPI]
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case reporterId = "reporter_id"
        case relatedChatId = "related_chat_id"
        case relatedCarId = "related_car_id"
        case status
        case driver1Info = "driver1_info"
        case driver2Info = "driver2_info"
        case vehicleDamage = "vehicle_damage"
        case accidentDescription = "accident_description"
        case insuranceInfo = "insurance_info"
        case otherInfo = "other_info"
        case signatureUrl = "signature_url"
        case signatureSignedAt = "signature_signed_at"
        case submittedAt = "submitted_at"
        case attachments
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        reporterId = try c.decode(UUID.self, forKey: .reporterId)
        relatedChatId = try c.decodeIfPresent(UUID.self, forKey: .relatedChatId)
        relatedCarId = try c.decodeIfPresent(UUID.self, forKey: .relatedCarId)
        status = try c.decode(String.self, forKey: .status)
        driver1Info = try c.decodeIfPresent(DriverInfoAPI.self, forKey: .driver1Info)
        driver2Info = try c.decodeIfPresent(DriverInfoAPI.self, forKey: .driver2Info)
        vehicleDamage = try c.decodeIfPresent(VehicleDamageAPI.self, forKey: .vehicleDamage)
        accidentDescription = try c.decodeIfPresent(String.self, forKey: .accidentDescription)
        insuranceInfo = try c.decodeIfPresent(InsuranceInfoAPI.self, forKey: .insuranceInfo)
        otherInfo = try c.decodeIfPresent(OtherInfoAPI.self, forKey: .otherInfo)
        signatureUrl = try c.decodeIfPresent(String.self, forKey: .signatureUrl)
        signatureSignedAt = try c.decodeIfPresent(Date.self, forKey: .signatureSignedAt)
        submittedAt = try c.decodeIfPresent(Date.self, forKey: .submittedAt)
        attachments = try c.decodeIfPresent([AccidentAttachmentAPI].self, forKey: .attachments) ?? []
        createdAt = try c.decode(Date.self, forKey: .createdAt)
        updatedAt = try c.decode(Date.self, forKey: .updatedAt)
    }
}

struct AccidentListResponse: Codable {
    let accidents: [AccidentAPIResponse]
}

struct AccidentAttachmentAPI: Codable, Identifiable {
    let id: UUID
    let accidentId: UUID
    let slot: String
    let fileUrl: String
    let fileSize: Int64
    let mimeType: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case accidentId = "accident_id"
        case slot
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case mimeType = "mime_type"
        case createdAt = "created_at"
    }
}

struct DriverInfoAPI: Codable {
    var driverLicenseId: String = ""
    var stateOfLicense: String = ""
    var driverName: String = ""
    var address: String = ""
    var city: String = ""
    var state: String = ""
    var zip: String = ""
    var dob: String = ""
    var peopleInVehicle: String = ""
    var publicPropertyDamaged: String = ""
    var injuries: String = ""
    var registrantName: String = ""
    var registrantAddress: String = ""
    var registrantCity: String = ""
    var registrantState: String = ""
    var registrantZip: String = ""
    var plateNumber: String = ""
    var stateOfReg: String = ""
    var vehicleYearMake: String = ""
    var vehicleType: String = ""
    var insCode: String = ""

    enum CodingKeys: String, CodingKey {
        case driverLicenseId = "driver_license_id"
        case stateOfLicense = "state_of_license"
        case driverName = "driver_name"
        case address, city, state, zip, dob
        case peopleInVehicle = "people_in_vehicle"
        case publicPropertyDamaged = "public_property_damaged"
        case injuries
        case registrantName = "registrant_name"
        case registrantAddress = "registrant_address"
        case registrantCity = "registrant_city"
        case registrantState = "registrant_state"
        case registrantZip = "registrant_zip"
        case plateNumber = "plate_number"
        case stateOfReg = "state_of_reg"
        case vehicleYearMake = "vehicle_year_make"
        case vehicleType = "vehicle_type"
        case insCode = "ins_code"
    }
}

struct VehicleDamageAPI: Codable {
    var description: String = ""
    var diagram: Int = 0
}

struct InsuranceInfoAPI: Codable {
    var insuranceCompany: String = ""
    var vin: String = ""
    var policyNumber: String = ""
    var policyPeriodFrom: String = ""
    var policyPeriodTo: String = ""

    enum CodingKeys: String, CodingKey {
        case insuranceCompany = "insurance_company"
        case vin
        case policyNumber = "policy_number"
        case policyPeriodFrom = "policy_period_from"
        case policyPeriodTo = "policy_period_to"
    }
}

struct OtherInfoAPI: Codable {
    var month: String = ""
    var day: String = ""
    var year: String = ""
    var dayOfWeek: String = ""
    var time: String = ""
    var numVehicles: String = ""
    var numInjured: String = ""
    var numKilled: String = ""
    var policeInvestigated: String = ""

    enum CodingKeys: String, CodingKey {
        case month, day, year
        case dayOfWeek = "day_of_week"
        case time
        case numVehicles = "num_vehicles"
        case numInjured = "num_injured"
        case numKilled = "num_killed"
        case policeInvestigated = "police_investigated"
    }
}

struct CreateAccidentRequest: Encodable {
    let relatedChatId: UUID?
    let relatedCarId: UUID?

    enum CodingKeys: String, CodingKey {
        case relatedChatId = "related_chat_id"
        case relatedCarId = "related_car_id"
    }
}

struct AccidentPatchRequest: Encodable {
    var driver1Info: DriverInfoAPI?
    var driver2Info: DriverInfoAPI?
    var vehicleDamage: VehicleDamageAPI?
    var accidentDescription: String?
    var insuranceInfo: InsuranceInfoAPI?
    var otherInfo: OtherInfoAPI?

    enum CodingKeys: String, CodingKey {
        case driver1Info = "driver1_info"
        case driver2Info = "driver2_info"
        case vehicleDamage = "vehicle_damage"
        case accidentDescription = "accident_description"
        case insuranceInfo = "insurance_info"
        case otherInfo = "other_info"
    }
}

struct SignatureUploadResponse: Decodable {
    let signatureUrl: String
    enum CodingKeys: String, CodingKey { case signatureUrl = "signature_url" }
}
