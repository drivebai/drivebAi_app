import Foundation
import Security

enum KeychainError: Error {
    case duplicateItem
    case itemNotFound
    case unexpectedStatus(OSStatus)
    case invalidData
}

final class KeychainService {
    static let shared = KeychainService()

    private let service = "com.drivebai.app"

    private enum Keys {
        static let accessToken = "access_token"
        static let refreshToken = "refresh_token"
        static let tokenExpiry = "token_expiry"
    }

    private init() {}

    // MARK: - Token Management

    func saveTokens(accessToken: String, refreshToken: String, expiresAt: Date) throws {
        try save(key: Keys.accessToken, data: accessToken.data(using: .utf8)!)
        try save(key: Keys.refreshToken, data: refreshToken.data(using: .utf8)!)

        let expiryData = try JSONEncoder().encode(expiresAt)
        try save(key: Keys.tokenExpiry, data: expiryData)
    }

    func getAccessToken() -> String? {
        guard let data = try? get(key: Keys.accessToken) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func getRefreshToken() -> String? {
        guard let data = try? get(key: Keys.refreshToken) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func getTokenExpiry() -> Date? {
        guard let data = try? get(key: Keys.tokenExpiry) else { return nil }
        return try? JSONDecoder().decode(Date.self, from: data)
    }

    func isAccessTokenExpired() -> Bool {
        guard let expiry = getTokenExpiry() else { return true }
        return Date() >= expiry
    }

    func clearTokens() {
        try? delete(key: Keys.accessToken)
        try? delete(key: Keys.refreshToken)
        try? delete(key: Keys.tokenExpiry)
    }

    // MARK: - Generic Keychain Operations

    private func save(key: String, data: Data) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        ]

        // Delete any existing item first
        SecItemDelete(query as CFDictionary)

        let status = SecItemAdd(query as CFDictionary, nil)

        guard status == errSecSuccess else {
            throw KeychainError.unexpectedStatus(status)
        }
    }

    private func get(key: String) throws -> Data {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]

        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)

        guard status == errSecSuccess else {
            if status == errSecItemNotFound {
                throw KeychainError.itemNotFound
            }
            throw KeychainError.unexpectedStatus(status)
        }

        guard let data = result as? Data else {
            throw KeychainError.invalidData
        }

        return data
    }

    private func delete(key: String) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key
        ]

        let status = SecItemDelete(query as CFDictionary)

        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.unexpectedStatus(status)
        }
    }
}
