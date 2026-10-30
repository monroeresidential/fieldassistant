import FieldAssistantAPI
import Foundation

/// The work a tapped Submit owes the server for one draft, in a fixed order: save the reviewed
/// payload, delete the photos the user removed, upload the pending photos one at a time in capture
/// order, post the submit, then poll its job. Review runs it inline and `DraftSync` runs it for
/// every queued draft (`submitPending`), so a Submit tapped with no signal goes out on its own.
/// Every step is idempotent on the server, so a run cut short anywhere is safe to repeat.
@MainActor
final class SubmitRunner {
    enum Outcome: Equatable {
        /// The server answered 200 or 202 (and the poll, if any, finished); it owns the draft now.
        /// Carries the server's copy of the draft as it stands: the poll's answer, or, when the poll
        /// failed, the last copy the phone has, marked `submitting` (`nil` if the phone has none).
        case accepted(DraftResponse?)
        /// The draft stays queued (the flag is kept) and a later pass tries again; the reason says why.
        case stillPending(PendingReason)
        /// The server refused the draft for good: the flag is cleared and the draft persisted `failed`.
        case refused(APIFailure)
        /// A photo upload was refused for good: the flag is cleared, the photo and the draft are `failed`.
        case photoFailed(LocalPhoto)
    }

    /// Why a run left the draft queued.
    enum PendingReason: Equatable {
        /// No connection: nothing else can be sent either.
        case offline
        /// No definitive answer for this draft (a 5xx, a cancelled run, a transient file read).
        case server
        /// The session needs signing in again; the draft sends after the next sign-in.
        case reauthRequired
        /// Another run for the draft is in progress and owns it.
        case alreadyRunning
    }

    /// The backend's 400 for a change to a draft that is submitting or submitted (`ErrDraftLocked`).
    static let lockedMessage = "draft is submitted or being submitted"
    /// Why a photo whose file the phone cannot read is not sent.
    static let missingFileMessage = "Photo file is missing on this phone."
    /// A photo the server refused earlier without saying why.
    static let photoRefusedMessage = "Couldn't send this photo."

    private let store: DraftStore
    private let service: @MainActor () -> DraftService
    /// The work of each draft with a run in progress, so `cancelAll` can stop it.
    private var running: [UUID: Task<Void, Never>] = [:]

    init(store: DraftStore, service: @escaping @MainActor () -> DraftService) {
        self.store = store
        self.service = service
    }

    func isRunning(_ draftID: UUID) -> Bool {
        running[draftID] != nil
    }

    /// Runs the queued work for `draft`. A draft that already has a run in progress returns
    /// `.stillPending(.alreadyRunning)` at once: that run owns it. Cancelling the caller, or
    /// `cancelAll`, cancels the work; a cancelled run ends as `.stillPending` unless the server has
    /// already accepted the submit.
    func run(_ draft: LocalDraft) async -> Outcome {
        let id = draft.id
        guard running[id] == nil else { return .stillPending(.alreadyRunning) }
        let result = OutcomeBox()
        let service = service()
        // The work runs as its own task so `cancelAll` can reach it whoever started it. `Outcome`
        // holds a model object and is not `Sendable`, so the task leaves it in the box.
        let work = Task { @MainActor in
            result.outcome = await self.perform(draft, service)
        }
        running[id] = work
        await withTaskCancellationHandler {
            await work.value
        } onCancel: {
            work.cancel()
        }
        if running[id] == work { running[id] = nil }
        return result.outcome ?? .stillPending(.server)
    }

    /// Cancels every run in progress (the session ended): each ends `.stillPending` with its draft
    /// still queued for its owner's next sign-in.
    func cancelAll() {
        for work in running.values { work.cancel() }
        running.removeAll()
    }

    private func perform(_ draft: LocalDraft, _ service: DraftService) async -> Outcome {
        if let stop = await savePayload(draft, service) { return stop }
        if let stop = await deleteRemovedPhotos(draft, service) { return stop }
        if let stop = await uploadPendingPhotos(draft, service) { return stop }
        return await submit(draft, service)
    }

    // MARK: - Steps

    /// PATCHes the payload Review stored on the phone, if any.
    private func savePayload(_ draft: LocalDraft, _ service: DraftService) async -> Outcome? {
        guard let data = draft.pendingPayloadData else { return nil }
        guard let payload = try? LocalDraft.decoder.decode(RFIDraftPayload.self, from: data) else {
            // Never expected: an unreadable payload must not hold the queue; the server keeps its copy.
            draft.pendingPayloadData = nil
            save()
            return nil
        }
        do {
            let response = try await service.save(id: draft.id, companyID: draft.companyID, payload: payload)
            applyKeepingQueued(response, to: draft)
            draft.pendingPayloadData = nil
            save()
            return nil
        } catch {
            return await settle(error, draft, service)
        }
    }

    /// DELETEs each photo the user removed, then drops its row and file. The server clears a
    /// photo-failed draft's flag once the photo is gone, so its copy is read back afterwards.
    private func deleteRemovedPhotos(_ draft: LocalDraft, _ service: DraftService) async -> Outcome? {
        let removed = draft.photos.filter { $0.uploadState == .removed }
        guard !removed.isEmpty else { return nil }
        for photo in removed {
            do {
                try await service.deleteAttachment(draftID: draft.id, attachmentID: photo.id, companyID: draft.companyID)
            } catch {
                // A 404: the server does not have it, which is what the DELETE was for.
                if case .notFound = APIFailure(error) {} else { return await settle(error, draft, service) }
            }
            try? FileManager.default.removeItem(at: DraftFiles.photoURL(draftID: draft.id, fileName: photo.fileName))
            draft.photos.removeAll { $0.id == photo.id }
            photo.modelContext?.delete(photo)
            save()
        }
        // Best effort: the submit's own answer settles the status if this read fails.
        if let response = try? await service.get(id: draft.id, companyID: draft.companyID) {
            applyKeepingQueued(response, to: draft)
            save()
        }
        return nil
    }

    /// PUTs each pending photo, oldest first, one at a time. A photo refused for good stops the run
    /// before the submit, as does one the server refused on an earlier run.
    private func uploadPendingPhotos(_ draft: LocalDraft, _ service: DraftService) async -> Outcome? {
        for photo in draft.photos.inCaptureOrder() where photo.uploadState == .pending {
            let fileURL = DraftFiles.photoURL(draftID: draft.id, fileName: photo.fileName)
            do {
                let attachment = try await service.uploadAttachment(
                    draftID: draft.id, attachmentID: photo.id, companyID: draft.companyID, fileURL: fileURL
                )
                photo.uploadState = .stored
                photo.bytes = attachment.bytes
                photo.failureMessage = nil
                save()
            } catch let error as CocoaError where Self.missingFileCodes.contains(error.code) {
                // The file is gone: no retry can send it.
                return failPhoto(photo, message: Self.missingFileMessage, draft: draft)
            } catch is CocoaError {
                // Any other read error may pass (the phone locked with data protection, say).
                return .stillPending(.server)
            } catch {
                let failure = APIFailure(error)
                switch failure {
                case .validation(Self.lockedMessage):
                    return await settle(error, draft, service)
                case .validation(let message), .notFound(let message):
                    return failPhoto(photo, message: message, draft: draft)
                case .offline, .server, .permissionDenied, .reauthRequired:
                    return await settle(error, draft, service)
                }
            }
        }
        if let failed = draft.photos.inCaptureOrder().first(where: { $0.uploadState == .failed }) {
            return failPhoto(failed, message: failed.failureMessage ?? Self.photoRefusedMessage, draft: draft)
        }
        return nil
    }

    /// POSTs the submit and, when the server queues it, polls the job.
    private func submit(_ draft: LocalDraft, _ service: DraftService) async -> Outcome {
        let start: DraftService.SubmitStart
        do {
            start = try await service.submit(id: draft.id, companyID: draft.companyID)
        } catch {
            return await settle(error, draft, service)
        }
        switch start {
        case .submitted(let response):
            return accept(response, draft: draft)
        case .running(let jobID):
            // The server has queued the submit and owns the draft from here, whatever the poll says.
            draft.submitPending = false
            save()
            do {
                let response = try await service.waitForJob(id: jobID, draftID: draft.id, companyID: draft.companyID)
                return accept(response, draft: draft)
            } catch {
                // The draft as it stands: queued on the server. Reconcile settles it later.
                draft.status = .submitting
                save()
                guard var response = draft.server else { return .accepted(nil) }
                response.status = .submitting
                return .accepted(response)
            }
        }
    }

    // MARK: - Outcomes

    /// Maps a failed save, delete, upload or submit: no definitive answer, or a session that needs
    /// signing in again, keeps the draft queued; the locked 400 means an earlier submit got through;
    /// anything else (including a 404: the server has lost the draft) is a refusal.
    private func settle(_ error: Error, _ draft: LocalDraft, _ service: DraftService) async -> Outcome {
        let failure = APIFailure(error)
        switch failure {
        case .offline:
            return .stillPending(.offline)
        case .server:
            return .stillPending(.server)
        case .reauthRequired:
            return .stillPending(.reauthRequired)
        case .validation(Self.lockedMessage):
            return await takeServerCopy(draft, service)
        case .validation, .permissionDenied, .notFound:
            return await refuse(draft, failure: failure, service)
        }
    }

    /// The server refused a change because the draft is already submitting or submitted (an earlier
    /// POST landed but its answer was lost): takes the server's copy as an accepted submit.
    private func takeServerCopy(_ draft: LocalDraft, _ service: DraftService) async -> Outcome {
        guard let response = try? await service.get(id: draft.id, companyID: draft.companyID) else { return .stillPending(.server) }
        draft.pendingPayloadData = nil
        return accept(response, draft: draft)
    }

    private func accept(_ response: DraftResponse, draft: LocalDraft) -> Outcome {
        draft.apply(response)
        draft.submitPending = false
        save()
        if response.status == .submitted { DraftFiles.removeAll(for: draft.id) }
        return .accepted(response)
    }

    /// Records a definitive refusal on the phone, so a draft with no screen open reads Fix with the
    /// server's reason, then reads the server's copy so its flags (`unresolved[]`) are kept too.
    private func refuse(_ draft: LocalDraft, failure: APIFailure, _ service: DraftService) async -> Outcome {
        let message = failure.userMessage
        draft.submitPending = false
        draft.status = .failed
        draft.errorMessage = message
        save()
        if let response = try? await service.get(id: draft.id, companyID: draft.companyID) {
            draft.apply(response)
            // The server keeps a refused draft `reviewed`; the phone keeps it as Fix with the reason.
            if draft.status == .draft || draft.status == .failed {
                draft.status = .failed
                draft.errorMessage = message
            }
            save()
        }
        return .refused(failure)
    }

    private func failPhoto(_ photo: LocalPhoto, message: String, draft: LocalDraft) -> Outcome {
        photo.uploadState = .failed
        photo.failureMessage = message
        draft.submitPending = false
        draft.status = .failed
        draft.errorMessage = message
        save()
        return .photoFailed(photo)
    }

    /// Applies a server response mid-run: the server still has the draft `reviewed`, but on the
    /// phone it stays Queued until the submit is answered.
    private func applyKeepingQueued(_ response: DraftResponse, to draft: LocalDraft) {
        draft.apply(response)
        draft.status = .submitting
    }

    private func save() {
        try? store.save()
    }

    /// The read errors that mean the photo file is not on the phone.
    private static let missingFileCodes: Set<CocoaError.Code> = [.fileReadNoSuchFile, .fileNoSuchFile]
}

/// Carries a run's outcome out of its task on the main actor.
@MainActor
private final class OutcomeBox {
    var outcome: SubmitRunner.Outcome?
}
