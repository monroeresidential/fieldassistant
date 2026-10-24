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

    var body: some View {
        VStack(spacing: 0) {
            transcriptArea
            Divider()
            photoStrip
            Divider()
            bottomRow
        }
        .navigationBarBackButtonHidden(true)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { toolbar }
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

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            Button("Cancel") {
                model.discard()
                onLeave()
            }
            .frame(minWidth: 44, minHeight: 44)
            .disabled(isBuilding)
            .accessibilityLabel("Cancel recording")
            .accessibilityHint("Discards this recording and its photos")
        }
        ToolbarItem(placement: .principal) {
            statusLabel
        }
        #if DEBUG
        ToolbarItem(placement: .topBarTrailing) {
            Button("Type instead") {
                typedText = model.transcript
                showsTyping = true
            }
            .frame(minWidth: 44, minHeight: 44)
            .disabled(isBuilding)
            .accessibilityLabel("Type instead")
        }
        #endif
    }

    @ViewBuilder
    private var statusLabel: some View {
        switch model.state {
        case .idle, .requestingPermission:
            Text("Starting…").font(.headline)
        case .recording:
            HStack(spacing: 6) {
                Image(systemName: "circle.fill")
                    .font(.caption2)
                    .foregroundStyle(Color.accentColor)
                    .symbolEffect(.pulse)
                    .accessibilityHidden(true)
                Text("Recording \(Self.clock(model.elapsed))")
                    .font(.headline.monospacedDigit())
            }
            .accessibilityElement(children: .combine)
            .accessibilityLabel("Recording, \(Self.spokenDuration(model.elapsed))")
        case .stopped:
            Text(buildPhase == .idle ? "Stopped" : "Building…").font(.headline)
        case .denied, .failed:
            Text("Not recording").font(.headline)
        }
    }

    // MARK: - Transcript

    private var transcriptArea: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    switch model.state {
                    case .denied(let reason):
                        deniedMessage(reason)
                    case .failed(let message):
                        failedMessage(message)
                    default:
                        if model.transcript.isEmpty {
                            Text("Start talking. Say what you need answered, where it is, and who should answer it.")
                                .font(.title3)
                                .foregroundStyle(.secondary)
                        } else {
                            Text(model.transcript)
                                .font(.title3.weight(.medium))
                                .textSelection(.enabled)
                        }
                    }
                    Color.clear.frame(height: 1).id("end")
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()
            }
            .onChange(of: model.transcript) {
                proxy.scrollTo("end", anchor: .bottom)
            }
        }
    }

    private func deniedMessage(_ reason: TranscriberAuthorization) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(Self.deniedTitle(reason), systemImage: "mic.slash")
                .font(.title3.weight(.semibold))
            Text(Self.deniedExplanation(reason))
                .foregroundStyle(.secondary)
            if reason != .unsupported, let url = URL(string: UIApplication.openSettingsURLString) {
                Link(destination: url) {
                    Text("Open Settings")
                        .font(.headline)
                        .frame(minWidth: 160, minHeight: 44)
                }
                .buttonStyle(.borderedProminent)
                .accessibilityLabel("Open Settings")
            }
            retryButton
        }
    }

    private func failedMessage(_ message: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Recording stopped", systemImage: "exclamationmark.triangle")
                .font(.title3.weight(.semibold))
            Text(message).foregroundStyle(.secondary)
            if !model.transcript.isEmpty {
                Text(model.transcript).font(.title3.weight(.medium))
            }
            retryButton
        }
    }

    private var retryButton: some View {
        Button {
            onRetry()
        } label: {
            Text("Try again")
                .font(.headline)
                .frame(minWidth: 160, minHeight: 44)
        }
        .buttonStyle(.bordered)
        .accessibilityLabel("Try recording again")
    }

    // MARK: - Photos

    private var photoStrip: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(model.photos.isEmpty ? "Photos" : "Photos · \(model.photos.count)")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                if model.coordinate != nil {
                    Label("Location tagged", systemImage: "location.fill")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
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
        .padding(.horizontal)
        .padding(.vertical, 8)
    }

    private nonisolated static func addTile(systemImage: String) -> some View {
        RoundedRectangle(cornerRadius: 6)
            .strokeBorder(style: StrokeStyle(lineWidth: 1.5, dash: [4]))
            .foregroundStyle(.secondary)
            .frame(width: 76, height: 76)
            .overlay {
                Image(systemName: systemImage)
                    .font(.title2)
                    .foregroundStyle(Color.accentColor)
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

    // MARK: - Bottom row

    private var bottomRow: some View {
        HStack(spacing: 16) {
            Button {
                model.stop()
            } label: {
                ZStack {
                    Circle()
                        .fill(Color.accentColor.opacity(model.state == .recording ? 0.15 : 0))
                        .frame(width: 92, height: 92)
                    Circle()
                        .fill(model.state == .recording ? Color.accentColor : Color(.systemGray4))
                        .frame(width: 76, height: 76)
                    Image(systemName: "stop.fill")
                        .font(.title)
                        .foregroundStyle(.white)
                }
            }
            .buttonStyle(.plain)
            .disabled(model.state != .recording)
            .accessibilityLabel("Stop recording")

            Button {
                Task { await build() }
            } label: {
                HStack(spacing: 8) {
                    if buildPhase == .sending || showsFinishing { ProgressView().tint(.white) }
                    Text(buildTitle)
                        .font(.headline)
                }
                .frame(maxWidth: .infinity, minHeight: 52)
            }
            .buttonStyle(.borderedProminent)
            .disabled(!model.canBuild || isBuilding)
            .accessibilityLabel(buildTitle)
            .accessibilityHint(model.canBuild ? "Sends the transcript to build the RFI draft" : "Stop recording first")
        }
        .padding(.horizontal)
        .padding(.vertical, 12)
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
                .font(.title3)
                .padding()
                .navigationTitle("Type instead")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { showsTyping = false }
                            .frame(minWidth: 44, minHeight: 44)
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Use this") {
                            model.setTypedTranscript(typedText)
                            showsTyping = false
                        }
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

/// A square photo from a draft's folder, decoded off the main thread at thumbnail size.
struct PhotoThumbnail: View {
    let url: URL
    let side: CGFloat

    @State private var image: UIImage?

    var body: some View {
        RoundedRectangle(cornerRadius: 6)
            .fill(Color(.secondarySystemBackground))
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
