import CoreLocation
import Foundation

/// One GPS fix for a capture. Asks for when-in-use permission only, never "always", and gives up
/// with `nil` on denial, error or timeout so capture never waits on location.
@MainActor
final class LocationOnce: NSObject, CLLocationManagerDelegate {
    private let manager: CLLocationManager?
    private let stubbed: CLLocationCoordinate2D?
    private var pending: CheckedContinuation<CLLocationCoordinate2D?, Never>?

    override init() {
        manager = CLLocationManager()
        stubbed = nil
        super.init()
        manager?.delegate = self
        manager?.desiredAccuracy = kCLLocationAccuracyNearestTenMeters
    }

    /// A location source that answers `coordinate` without touching Core Location; for tests.
    init(stub coordinate: CLLocationCoordinate2D?) {
        manager = nil
        stubbed = coordinate
        super.init()
    }

    func fix(timeout: Duration = .seconds(5)) async -> CLLocationCoordinate2D? {
        guard let manager else { return stubbed }
        guard pending == nil else { return nil }
        let timer = Task { [weak self] in
            try? await Task.sleep(for: timeout)
            guard !Task.isCancelled else { return }
            self?.finish(nil)
        }
        defer { timer.cancel() }
        return await withCheckedContinuation { continuation in
            pending = continuation
            switch manager.authorizationStatus {
            case .notDetermined:
                manager.requestWhenInUseAuthorization()
            case .authorizedWhenInUse, .authorizedAlways:
                manager.requestLocation()
            default:
                finish(nil)
            }
        }
    }

    private func finish(_ coordinate: CLLocationCoordinate2D?) {
        pending?.resume(returning: coordinate)
        pending = nil
    }

    // The manager was created on the main thread, so it calls its delegate there.

    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        let status = manager.authorizationStatus
        MainActor.assumeIsolated {
            guard pending != nil else { return }
            switch status {
            case .notDetermined:
                break
            case .authorizedWhenInUse, .authorizedAlways:
                self.manager?.requestLocation()
            default:
                finish(nil)
            }
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        let coordinate = locations.last?.coordinate
        MainActor.assumeIsolated { finish(coordinate) }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {
        MainActor.assumeIsolated { finish(nil) }
    }
}
