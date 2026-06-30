# DriveBaiWidgets — Xcode wire-up (one-time)

This folder contains the source for the DriveBai Live Activity widget bundle.
The Swift files are staged here, but they are NOT yet members of any Xcode
target — adding a Widget Extension via hand-edited `project.pbxproj` is
fragile, so we use Xcode's wizard.

## One-time setup (≈60 seconds)

1. Open `DriveBai.xcodeproj` in Xcode.
2. **File → New → Target…**
3. Pick **Widget Extension** under iOS → Application Extension. Click Next.
4. Configure:
   - Product Name: **`DriveBaiWidgets`** (must match the folder name).
   - Team: select your DriveBai team (matches the host app — 48YP84XNBV).
   - Bundle Identifier: leave Xcode's default `com.drivebai-ios.app.DriveBaiWidgets`.
   - Language: **Swift**.
   - Include Live Activity: **✓ checked**.
   - Include Configuration App Intent: **✗ unchecked** (we don't use it).
5. Click **Finish**. When prompted to activate the new scheme, click **Activate**.
6. Xcode generates a default folder with placeholder files. **Delete those generated files** (keep the folder, keep the Info.plist and Assets.xcassets that Xcode creates).
7. In the Project navigator, right-click the **DriveBaiWidgets** group → **Add Files to "DriveBai"…**
8. Select the two Swift files in this folder:
   - `DriveBaiWidgetsBundle.swift`
   - `PickupLiveActivity.swift`
9. In the Add dialog:
   - Destination: **Copy items if needed: ✗ unchecked** (the files are already here).
   - Add to targets: **DriveBaiWidgets only** (uncheck DriveBai).
10. Add the shared attributes file to BOTH targets:
    - In the Project navigator, navigate to `DriveBai/Sources/LiveActivity/PickupActivityAttributes.swift`.
    - Open the **File inspector** (right panel, first tab).
    - Under **Target Membership**, check both **DriveBai** AND **DriveBaiWidgets**.
11. Make sure the widget's deployment target matches the host (iOS 17.0):
    - Select the `DriveBaiWidgets` target → **General** tab → **Minimum Deployments: iOS 17.0**.
12. (Optional but recommended) Mirror the brand color so the widget tint matches the host:
    - Open `DriveBaiWidgets/Assets.xcassets` (the one Xcode generated) → add a new color set named **`AccentColor`** with the same hex as the host's brand teal (`Color("AccentColor", bundle: nil)` in `PickupLiveActivity.swift` falls back to system tint if missing — works either way, but matches the in-app card if you add it).
13. Build & run. The host app's `PickupLiveActivityManager` is already wired and will start the activity on payment-completed automatically.

## Verifying the wire-up

After the steps above, `xcodebuild -scheme DriveBai build` should succeed
with one additional target (`DriveBaiWidgets`) being compiled and embedded
as an `.appex` inside the main app bundle.

### Smoke test on a device or simulator

1. Settings → Live Activities → make sure DriveBai is allowed.
2. In the app, complete a test rental flow:
   - Driver creates request → owner accepts → driver pays with Stripe test card `4242 4242 4242 4242`.
3. As soon as Stripe confirms, lock the device or pull down Notification Center.
4. You should see the **DriveBai pickup card** with the live countdown,
   progress bar, and (on iPhone 14 Pro and later) a Dynamic Island
   indicator with the timer in the trailing region.
5. Tap the activity → DriveBai opens straight to Chat → Requests for the
   active rental (`drivebai://lease/{id}/pickup`).
6. Tap "I've picked up the car" inside the app → activity flips briefly to
   "Pickup confirmed" / green, then dismisses.

## What lives where

| File | Target | Purpose |
|------|--------|---------|
| `DriveBai/Sources/LiveActivity/PickupActivityAttributes.swift` | **Both** | `ActivityAttributes` + `ContentState`. Shared so the widget can read what the manager writes. |
| `DriveBai/Sources/LiveActivity/PickupLiveActivityManager.swift` | DriveBai | Singleton that owns `start/update/end` calls. Trigger sites live in `ChatView`, `ChatViewModel`, `DriverTodayViewModel`, `OwnerTodayViewModel`. |
| `DriveBaiWidgets/DriveBaiWidgetsBundle.swift` | DriveBaiWidgets | `@main` entry point. |
| `DriveBaiWidgets/PickupLiveActivity.swift` | DriveBaiWidgets | `ActivityConfiguration` + Lock Screen view + Dynamic Island regions. |

## Notes

- No App Group is required for the current local-only update path.
- No remote push tokens for MVP — all start/update/end happens in-process.
- The Info.plist key `NSSupportsLiveActivities` is set on the host app's
  Info.plist (already committed). Without it, `Activity.request(...)` would
  throw at runtime.
- If you skip step 12 (AccentColor in the widget asset catalog), the
  widget falls back to the system accent for the normal-tier color — still
  readable, just not brand teal. The warning/critical colors (orange/red)
  are system colors and don't need a brand asset.
