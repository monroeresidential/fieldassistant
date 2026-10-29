import PhotosUI
import SwiftUI
import UIKit

/// Owns the capture model for one `Route.capture`. A model left denied or failed is replaced by a
/// fresh one on "Try again", so recording can start over.
struct CaptureScreen: View {
    let draftID: UUID
    let companyID: Int64
    let projectID: Int64
    /// The draft reached the server; open Review.
    let onBuilt: () -> Void
    /// Back to Home: cancelled, or the draft is waiting to be sent.
    let onLeave: () -> Void

    @State private var model: CaptureModel?

    var body: some View {
        Group {
            if let model {
                CaptureView(
                    model: model,
                    companyID: companyID,
                    projectID: projectID,
                    onBuilt: onBuilt,
                    onLeave: onLeave,
                    onRetry: { replaceModel() }
                )
            } else {
                Color.clear
            }
        }
        .onAppear {
            if model == nil { model = Self.makeModel(draftID: draftID) }
        }
    }

    private func replaceModel() {
        model?.discard()
        model = Self.makeModel(draftID: draftID)
    }

    private static func makeModel(draftID: UUID) -> CaptureModel {
        CaptureModel(draftID: draftID, transcriber: SpeechTranscriber(), location: LocationOnce())
    }
}

/// Record, watch the transcript, add photos, then build the RFI draft.
struct CaptureView: View {
    let model: CaptureModel
    let companyID: Int64
    let projectID: Int64
    let onBuilt: () -> Void
    let onLeave: () -> Void
    let onRetry: () -> Void

    private enum BuildPhase {
        case idle
        /// Waiting for the recognizer's final text after Stop.
        case finishing
        /// Waiting for the server to extract the draft.
        case sending
    }

    @Environment(AppState.self) private var appState
    @State private var buildPhase = BuildPhase.idle
    /// Shown only when the final transcript takes more than a moment.
    @State private var showsFinishing = false
    @State private var pickedPhotos: [PhotosPickerItem] = []
    @State private var showsCamera = false
    @State private var photoError: String?
    @State private var buildError: String?
    @State private var leavesAfterError = false
    /// Set once the draft is made from the model; photos that finish loading later are dropped.
    @State private var draftMade = false
    @State private var confirmsDiscard = false
    #if DEBUG
    @State private var showsTyping = false
    @State private var typedText = ""
    #endif

    init(
        model: CaptureModel,
        companyID: Int64,
        projectID: Int64,
        onBuilt: @escaping () -> Void = {},
        onLeave: @escaping () -> Void = {},
        onRetry: @escaping () -> Void = {}
    ) {
        self.model = model
        self.companyID = companyID
        self.projectID = projectID
        self.onBuilt = onBuilt
        self.onLeave = onLeave
        self.onRetry = onRetry
    }

    private var isBuilding: Bool { buildPhase != .idle }

    private var controls: ControlState { ControlState(for: model.state, isBuilding: isBuilding) }

    /// The handoff's 13/600 status line beside the blinking dot.
    private static let statusStyle = Theme.TextStyle(13, .semibold, relativeTo: .footnote, monospacedDigits: true)
    /// The "RFI" kicker on the right of the top bar: 11/700 .12em.
    private static let modeStyle = Theme.TextStyle(11, .bold, relativeTo: .caption2, em: 0.12)
    /// The location line and the footer caption: 11/400 tabular.
    private static let captionStyle = Theme.TextStyle(11, .regular, relativeTo: .caption2, monospacedDigits: true)
    private static let buildStyle = Theme.TextStyle(16, .bold, relativeTo: .headline)

    /// The caret's 24 pt height at the default text size; it grows with the transcript.
    @ScaledMetric(relativeTo: .title2) private var caretHeight: CGFloat = 24

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            topBar
            transcriptArea
            photoStrip
            footer
        }
        .padding(.horizontal, 20)
        // The handoff's 62 top and 48 bottom include the status bar and home indicator: the top
        // equals the safe area of its 402 x 874 frame, and 14 is what remains below the indicator.
        .padding(.bottom, 14)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(Theme.Color.ground.ignoresSafeArea())
        .toolbar(.hidden, for: .navigationBar)
        .navigationBarBackButtonHidden(true)
        .task(id: ObjectIdentifier(model)) { await model.start() }
        // Leaving by any route (sign-out swaps the root, a path change) must not leave the mic on.
        // Stop, not discard: a successful Build also leaves this screen and owns the files now.
        .onDisappear { if model.state == .recording { model.stop() } }
        .onChange(of: pickedPhotos) { _, items in
            guard !items.isEmpty else { return }
            Task { await addPicked(items) }
        }
        .fullScreenCover(isPresented: $showsCamera) {
            CameraPicker { image in
                add(image)
            }
            .ignoresSafeArea()
        }
        .alert("Couldn't add the photo", isPresented: Binding(get: { photoError != nil }, set: { if !$0 { photoError = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(photoError ?? "")
        }
        .alert("Couldn't build the RFI", isPresented: Binding(get: { buildError != nil }, set: { if !$0 { buildError = nil } })) {
            Button("OK", role: .cancel) {
                if leavesAfterError { onLeave() }
            }
        } message: {
            Text(buildError ?? "")
        }
        #if DEBUG
        .sheet(isPresented: $showsTyping) { typingSheet }
        #endif
    }

    // MARK: - Top bar

    /// Cancel, the recording status and the "RFI" kicker, spaced like the handoff's space-between.
    private var topBar: some View {
        HStack(spacing: 8) {
            cancelButton
            Spacer(minLength: 0)
            statusLabel
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            Spacer(minLength: 0)
            Text("RFI")
                .textStyle(Self.modeStyle)
                .foregroundStyle(Theme.Color.tertiary)
        }
        .frame(minHeight: 44)
    }

    private var cancelButton: some View {
        Button("Cancel") {
            if hasContent {
                confirmsDiscard = true
            } else {
                discardAndLeave()
            }
        }
        .buttonStyle(TextLinkButtonStyle(size: 15))
        .frame(minWidth: 44, minHeight: 44)
        .disabled(isBuilding)
        .accessibilityLabel("Cancel recording")
        .accessibilityHint("Discards this recording and its photos")
        .confirmationDialog("Discard this recording?", isPresented: $confirmsDiscard, titleVisibility: .visible) {
            Button("Discard", role: .destructive) { discardAndLeave() }
            Button("Keep recording", role: .cancel) {}
        } message: {
            Text("The transcript and photos are deleted from this phone.")
        }
    }

    /// Whether Cancel would throw away something the user made.
    private var hasContent: Bool {
        !model.transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !model.photos.isEmpty
    }

    private func discardAndLeave() {
        model.discard()
        onLeave()
    }

    private var statusLabel: some View {
        Group {
            switch model.state {
            case .idle, .requestingPermission:
                Text("Starting…")
            case .recording:
                HStack(spacing: 8) {
                    Blinking {
                        Circle()
                            .fill(Theme.Color.brand)
                            .frame(width: 10, height: 10)
                    }
                    .accessibilityHidden(true)
                    Text("Recording \(Self.clock(model.elapsed))")
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel("Recording, \(Self.spokenDuration(model.elapsed))")
            case .stopped:
                Text(buildPhase == .idle ? "Stopped" : "Building…")
            case .denied, .failed:
                Text("Not recording")
            }
        }
        .textStyle(Self.statusStyle)
        .foregroundStyle(Theme.Color.textPrimary)
    }

    // MARK: - Transcript

    private var transcriptArea: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    switch model.state {
                    case .denied(let reason):
                        transcriptHeader(showsLabel: false)
                        deniedMessage(reason)
                    case .failed(let message):
                        transcriptHeader(showsLabel: false)
                        failedMessage(message)
                    default:
                        transcriptHeader(showsLabel: true)
                        if let notice = model.notice, model.state == .stopped {
                            Label(notice, systemImage: "exclamationmark.triangle")
                                .textStyle(Theme.Text.meta13Medium)
                                .foregroundStyle(Theme.Color.accent)
                                .accessibilityLabel(notice)
                        }
                        if model.transcript.isEmpty {
                            Text("Start talking. Say what you need answered, where it is, and who should answer it.")
                                .textStyle(Theme.Text.transcript)
                                .foregroundStyle(Theme.Color.tertiary)
                        } else {
                            transcriptText
                        }
                    }
                    Color.clear.frame(height: 1).id("end")
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: .infinity)
            .onChange(of: model.transcript) {
                proxy.scrollTo("end", anchor: .bottom)
            }
        }
    }

    /// "Live transcript · on device", and in debug builds the Type instead link.
    @ViewBuilder
    private func transcriptHeader(showsLabel: Bool) -> some View {
        #if DEBUG
        // Side by side when both fit on one line; stacked at large text sizes.
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                if showsLabel { transcriptLabel }
                Spacer(minLength: 0)
                typeInsteadButton
            }
            VStack(alignment: .leading, spacing: 0) {
                if showsLabel { transcriptLabel }
                typeInsteadButton
            }
        }
        #else
        if showsLabel { transcriptLabel }
        #endif
    }

    #if DEBUG
    private var typeInsteadButton: some View {
        Button("Type instead") {
            typedText = model.transcript
            showsTyping = true
        }
        .buttonStyle(TextLinkButtonStyle(size: 13))
        .frame(minWidth: 44, minHeight: 44)
        .disabled(isBuilding)
        .accessibilityLabel("Type instead")
    }
    #endif

    private var transcriptLabel: some View {
        Kicker("Live transcript · on device", color: Theme.Color.muted)
    }

    /// The transcript, with the orange caret blinking after the last word while recording.
    /// The caret is drawn by an identical copy of the text laid over it with the words clear, so
    /// only that copy's opacity changes on each frame; both copies reserve the caret's width.
    @ViewBuilder
    private var transcriptText: some View {
        let words = Text(model.transcript)
        if model.state == .recording {
            let scale = caretHeight / 24
            let caret = Text(Image(uiImage: Self.caretImage(height: caretHeight)).renderingMode(.template))
                .baselineOffset(-4 * scale)
            Text("\(words.foregroundStyle(Theme.Color.textPrimary))\(caret.foregroundStyle(Color.clear))")
                .textStyle(Theme.Text.transcript)
                .textSelection(.enabled)
                .accessibilityLabel(model.transcript)
                .overlay(alignment: .topLeading) {
                    Blinking(delay: 0.3) {
                        Text("\(words.foregroundStyle(Color.clear))\(caret.foregroundStyle(Theme.Color.brand))")
                            .textStyle(Theme.Text.transcript)
                    }
                    .accessibilityHidden(true)
                    .allowsHitTesting(false)
                }
        } else {
            words
                .textStyle(Theme.Text.transcript)
                .foregroundStyle(Theme.Color.textPrimary)
                .textSelection(.enabled)
        }
    }

    /// A 3 pt bar with 3 pt of space before it, as a template image to sit inline in the text.
    private static func caretImage(height: CGFloat) -> UIImage {
        UIGraphicsImageRenderer(size: CGSize(width: 6, height: height)).image { context in
            context.fill(CGRect(x: 3, y: 0, width: 3, height: height))
        }
        .withRenderingMode(.alwaysTemplate)
    }

    private func deniedMessage(_ reason: TranscriberAuthorization) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(Self.deniedTitle(reason), systemImage: "mic.slash")
                .textStyle(Theme.Text.cardTitle)
                .foregroundStyle(Theme.Color.textPrimary)
            Text(Self.deniedExplanation(reason))
                .textStyle(Theme.Text.body)
                .foregroundStyle(Theme.Color.muted)
            if reason != .unsupported, let url = URL(string: UIApplication.openSettingsURLString) {
                Link(destination: url) {
                    Text("Open Settings")
                }
                .buttonStyle(PrimaryButtonStyle(height: 52))
                .accessibilityLabel("Open Settings")
            }
            retryButton
        }
    }

    private func failedMessage(_ message: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Recording stopped", systemImage: "exclamationmark.triangle")
                .textStyle(Theme.Text.cardTitle)
                .foregroundStyle(Theme.Color.textPrimary)
            Text(message)
                .textStyle(Theme.Text.body)
                .foregroundStyle(Theme.Color.muted)
            if !model.transcript.isEmpty {
                Text(model.transcript)
                    .textStyle(Theme.Text.transcript)
                    .foregroundStyle(Theme.Color.textPrimary)
            }
            retryButton
        }
    }

    private var retryButton: some View {
        Button {
            onRetry()
        } label: {
            Text("Try again")
        }
        .buttonStyle(SecondaryButtonStyle(height: 52))
        .accessibilityLabel("Try recording again")
    }

    // MARK: - Photos

    private var photoStrip: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Kicker(model.photos.isEmpty ? "Photos" : "Photos · \(model.photos.count)", color: Theme.Color.muted)
                Spacer(minLength: 0)
                if model.coordinate != nil {
                    Text("Location tagged")
                        .textStyle(Self.captionStyle)
                        .foregroundStyle(Theme.Color.tertiary)
                }
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(model.photos) { photo in
                        PhotoThumbnail(url: DraftFiles.photoURL(draftID: model.draftID, fileName: photo.fileName), side: 76)
                            .contextMenu {
                                Button("Remove photo", systemImage: "trash", role: .destructive) {
                                    model.removePhoto(photo)
                                }
                                .disabled(isBuilding)
                            }
                            .accessibilityLabel("Photo")
                            .accessibilityAction(named: "Remove photo") {
                                if !isBuilding { model.removePhoto(photo) }
                            }
                    }
                    PhotosPicker(selection: $pickedPhotos, matching: .images) {
                        Self.addTile(systemImage: "photo.on.rectangle")
                    }
                    .disabled(isBuilding)
                    .accessibilityLabel("Add photos from library")
                    if UIImagePickerController.isSourceTypeAvailable(.camera) {
                        Button {
                            showsCamera = true
                        } label: {
                            Self.addTile(systemImage: "camera")
                        }
                        .disabled(isBuilding)
                        .accessibilityLabel("Take a photo")
                    }
                }
            }
        }
    }

    private nonisolated static func addTile(systemImage: String) -> some View {
        RoundedRectangle(cornerRadius: 6)
            .strokeBorder(Theme.Color.dashedBorder, style: StrokeStyle(lineWidth: 1.5, dash: [4]))
            .frame(width: 76, height: 76)
            .overlay {
                Image(systemName: systemImage)
                    .font(.system(size: 26, weight: .medium))
                    .foregroundStyle(Theme.Color.muted)
            }
            .contentShape(Rectangle())
    }

    private func addPicked(_ items: [PhotosPickerItem]) async {
        pickedPhotos = []
        for item in items {
            guard let data = try? await item.loadTransferable(type: Data.self), let image = UIImage(data: data) else {
                photoError = "One of the photos could not be read."
                continue
            }
            add(image)
        }
    }

    private func add(_ image: UIImage) {
        guard !draftMade else { return }
        do {
            try model.addPhoto(image)
        } catch {
            photoError = error.localizedDescription
        }
    }

    // MARK: - Footer

    /// The stop button's halo ring, which the handoff draws as a shadow outside the 76 pt circle.
    private static let stopHalo: CGFloat = 8

    /// Stop and Build RFI over a hairline. The halo takes layout space here, so the gaps next to it
    /// are the handoff's 14 less the halo, keeping 14 pt between the circle and its neighbours.
    private var footer: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            HStack(spacing: 14 - Self.stopHalo) {
                RecordButton(glyph: .stop, size: 76, halo: Self.stopHalo, isEnabled: controls.stopEnabled) {
                    model.stop()
                }
                .accessibilityLabel("Stop recording")

                VStack(spacing: 6) {
                    buildButton
                    // Always laid out, so the Build button does not move when the caption hides.
                    Text("Stop recording to build. Works without signal.")
                        .textStyle(Self.captionStyle)
                        .foregroundStyle(Theme.Color.tertiary)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                        .opacity(showsStopCaption ? 1 : 0)
                        .accessibilityHidden(!showsStopCaption)
                }
            }
            .padding(.top, 14 - Self.stopHalo)
        }
    }

    private var buildButton: some View {
        Button {
            Task { await build() }
        } label: {
            HStack(spacing: 8) {
                if buildPhase == .sending || showsFinishing { ProgressView().tint(Theme.Color.tertiary) }
                Text(buildTitle)
                    .textStyle(Self.buildStyle)
            }
        }
        .buttonStyle(PrimaryButtonStyle(height: 52))
        .disabled(!controls.buildEnabled || !model.canBuild)
        .accessibilityLabel(buildTitle)
        .accessibilityHint(model.canBuild ? "Sends the transcript to build the RFI draft" : "Stop recording first")
    }

    /// The caption explains the disabled Build button while a recording is starting or running.
    private var showsStopCaption: Bool {
        switch model.state {
        case .idle, .requestingPermission, .recording: true
        case .stopped, .denied, .failed: false
        }
    }

    private var buildTitle: String {
        switch buildPhase {
        case .idle: "Build RFI"
        case .finishing: showsFinishing ? "Finishing…" : "Build RFI"
        case .sending: "Building…"
        }
    }

    // MARK: - Build

    /// Waits for the final transcript, stores the draft as waiting, and sends it for extraction.
    private func build() async {
        guard !isBuilding else { return }
        buildPhase = .finishing
        let reveal = Task {
            try? await Task.sleep(for: .milliseconds(400))
            if !Task.isCancelled { showsFinishing = true }
        }
        await model.waitForFinalTranscript()
        reveal.cancel()
        showsFinishing = false
        guard model.canBuild else {
            buildPhase = .idle
            return
        }

        guard let owner = appState.draftOwnerID else {
            buildPhase = .idle
            leavesAfterError = false
            buildError = APIFailure.reauthRequired.userMessage
            return
        }
        draftMade = true
        let draft = model.makeDraft(companyID: companyID, projectID: projectID, owner: owner)
        do {
            try appState.drafts.insert(draft)
        } catch {
            // Nothing was stored, so the capture stays open and can take photos and Build again.
            draftMade = false
            buildPhase = .idle
            leavesAfterError = false
            buildError = "The draft could not be saved on this phone. \(error.localizedDescription)"
            return
        }

        buildPhase = .sending
        switch await appState.send(draft) {
        case .sent:
            onBuilt()
        case .waiting:
            onLeave()
        case .failed(let message):
            // The draft is stored as waiting; Home lists it and a tap sends it again.
            leavesAfterError = true
            buildError = message
        }
    }

    // MARK: - Typing (debug)

    #if DEBUG
    private var typingSheet: some View {
        NavigationStack {
            TextEditor(text: $typedText)
                .textStyle(Theme.Text.transcript)
                .foregroundStyle(Theme.Color.textPrimary)
                .scrollContentBackground(.hidden)
                .padding()
                .background(Theme.Color.ground.ignoresSafeArea())
                .designNavigationTitle("Type instead")
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { showsTyping = false }
                            .font(Theme.Text.toolbarButton.font)
                            .frame(minWidth: 44, minHeight: 44)
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Use this") {
                            model.setTypedTranscript(typedText)
                            showsTyping = false
                        }
                        .font(Theme.Text.toolbarButton.font)
                        .frame(minWidth: 44, minHeight: 44)
                        .disabled(typedText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .accessibilityLabel("Use this transcript")
                    }
                }
        }
    }
    #endif

    // MARK: - Formatting

    static func clock(_ duration: Duration) -> String {
        let seconds = Int(duration.components.seconds)
        return String(format: "%d:%02d", seconds / 60, seconds % 60)
    }

    private static func spokenDuration(_ duration: Duration) -> String {
        let seconds = Int(duration.components.seconds)
        return "\(seconds / 60) minutes \(seconds % 60) seconds"
    }

    private static func deniedTitle(_ reason: TranscriberAuthorization) -> String {
        switch reason {
        case .deniedMicrophone: "Microphone is off"
        case .deniedSpeech: "Speech recognition is off"
        case .unsupported, .granted: "Dictation isn't available"
        }
    }

    private static func deniedExplanation(_ reason: TranscriberAuthorization) -> String {
        switch reason {
        case .deniedMicrophone:
            "Field Assistant needs the microphone to record your RFI. Turn it on in Settings, then try again."
        case .deniedSpeech:
            "Field Assistant turns your voice into text on this phone. Turn on speech recognition in Settings, then try again."
        case .unsupported, .granted:
            "This phone can't transcribe English on the device."
        }
    }
}

extension CaptureView {
    /// Which footer control is live. A control that is not live draws disabled, with the
    /// surface and tertiary tokens rather than a dimmed orange.
    struct ControlState: Equatable {
        let stopEnabled: Bool
        let buildEnabled: Bool
    }
}

extension CaptureView.ControlState {
    /// Stop is live only while recording and Build only once recording has stopped; neither while
    /// a build is under way (`isBuilding`: waiting for the final transcript or for the server).
    /// Build also needs a transcript, which the view checks with `CaptureModel.canBuild`.
    init(for state: CaptureModel.State, isBuilding: Bool) {
        stopEnabled = state == .recording && !isBuilding
        buildEnabled = state == .stopped && !isBuilding
    }
}

/// Fades its content out and back every 1.1 s (the handoff's blink), `delay` seconds behind a
/// blink with no delay. One clock drives every blink, so the dot and the caret stay in step.
/// Holds still with Reduce Motion on.
private struct Blinking<Content: View>: View {
    var delay: TimeInterval = 0
    @ViewBuilder let content: Content

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: reduceMotion)) { context in
            content.opacity(reduceMotion ? 1 : Self.opacity(at: context.date.timeIntervalSinceReferenceDate, delay: delay))
        }
    }

    /// 1 at the start of each 1.1 s period, 0 halfway, 1 again at the end.
    private static func opacity(at time: TimeInterval, delay: TimeInterval) -> Double {
        let phase = ((time - delay) / 1.1).truncatingRemainder(dividingBy: 1)
        return 0.5 + 0.5 * cos(2 * .pi * phase)
    }
}

/// A square photo from a draft's folder, decoded off the main thread at thumbnail size.
struct PhotoThumbnail: View {
    let url: URL
    let side: CGFloat

    @State private var image: UIImage?

    var body: some View {
        RoundedRectangle(cornerRadius: 6)
            .fill(Theme.Color.surface)
            .frame(width: side, height: side)
            .overlay {
                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFill()
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 6))
            .task(id: url) {
                let target = CGSize(width: side * 3, height: side * 3)
                image = await Task.detached(priority: .userInitiated) {
                    UIImage(contentsOfFile: url.path)?.preparingThumbnail(of: target)
                }.value
            }
    }
}

/// The system camera, returning the photo taken.
@MainActor
struct CameraPicker: UIViewControllerRepresentable {
    let onImage: (UIImage) -> Void
    @Environment(\.dismiss) private var dismiss

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ controller: UIImagePickerController, context: Context) {}

    func makeCoordinator() -> Coordinator {
        Coordinator(onImage: onImage, dismiss: { dismiss() })
    }

    @MainActor
    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let onImage: (UIImage) -> Void
        let dismiss: () -> Void

        init(onImage: @escaping (UIImage) -> Void, dismiss: @escaping () -> Void) {
            self.onImage = onImage
            self.dismiss = dismiss
        }

        func imagePickerController(
            _ picker: UIImagePickerController,
            didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]
        ) {
            if let image = info[.originalImage] as? UIImage { onImage(image) }
            dismiss()
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
            dismiss()
        }
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    NavigationStack {
        CaptureView(
            model: CaptureModel(transcriber: SpeechTranscriber()),
            companyID: 1,
            projectID: 7
        )
    }
    .environment(AppState.preview(container: container))
    .modelContainer(container)
}
