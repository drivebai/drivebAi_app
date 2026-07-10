import Foundation
import os

// MARK: - Product Tour Analytics
//
// No third-party analytics SDK exists in the app and none is added. This is a
// tiny internal protocol with a no-op default and an os_log implementation.
// The real funnel data comes for free from the server table's
// `status` / `last_step_key` / `updated_at` columns; if richer analytics is
// ever needed, a concrete implementation slots in behind this protocol without
// touching any call site.

protocol ProductTourAnalytics {
    func tourStarted(_ key: TourKey)
    func stepViewed(_ key: TourKey, step: String, index: Int)
    func tourSkipped(_ key: TourKey, atStep: String)
    func tourCompleted(_ key: TourKey)
}

/// Default: does nothing. Used in release builds.
struct NoopTourAnalytics: ProductTourAnalytics {
    func tourStarted(_ key: TourKey) {}
    func stepViewed(_ key: TourKey, step: String, index: Int) {}
    func tourSkipped(_ key: TourKey, atStep: String) {}
    func tourCompleted(_ key: TourKey) {}
}

/// Opt-in / DEBUG: writes structured lines to the unified log so a tour funnel
/// is inspectable in Console.app without any network dependency.
struct OSLogTourAnalytics: ProductTourAnalytics {
    private let log = Logger(subsystem: "llc.gnomon.drivebai", category: "ProductTour")

    func tourStarted(_ key: TourKey) {
        log.info("tour.start \(key.rawValue, privacy: .public)")
    }
    func stepViewed(_ key: TourKey, step: String, index: Int) {
        log.info("tour.step \(key.rawValue, privacy: .public) #\(index, privacy: .public) \(step, privacy: .public)")
    }
    func tourSkipped(_ key: TourKey, atStep: String) {
        log.info("tour.skip \(key.rawValue, privacy: .public) @\(atStep, privacy: .public)")
    }
    func tourCompleted(_ key: TourKey) {
        log.info("tour.done \(key.rawValue, privacy: .public)")
    }
}
