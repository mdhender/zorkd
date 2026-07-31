// Keeping the terminal looking like a terminal.
//
// Everything here is presentation. With it disabled the page still works: the
// form posts, the server answers, and the transcript is all there — the player
// just has to scroll to the bottom of it themselves.

(function () {
    "use strict";

    var transcript = document.getElementById("transcript");
    if (!transcript) {
        return;
    }

    function toBottom() {
        transcript.scrollTop = transcript.scrollHeight;
    }

    // A refresh redraws the whole game from the server, so the newest text is
    // at the bottom of a box that opens at the top. Start where the player left
    // off rather than at the copyright notice.
    toBottom();

    // And again after every turn htmx appends.
    document.body.addEventListener("htmx:afterSwap", toBottom);

    // Typing anywhere on the page means typing at the prompt. A terminal has
    // one place for input, and hunting for it with the mouse is not it.
    document.addEventListener("keydown", function (event) {
        var command = document.getElementById("command");
        if (!command || document.activeElement === command) {
            return;
        }
        if (event.ctrlKey || event.metaKey || event.altKey || event.key.length !== 1) {
            return;
        }
        // Leave the transcript alone when it is being read with the keyboard.
        if (document.activeElement === transcript) {
            return;
        }
        command.focus();
    });
})();
