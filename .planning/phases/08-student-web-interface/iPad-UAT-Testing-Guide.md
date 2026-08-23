# iPad UAT Testing Guide - Phase 08

**Purpose:** Execute the 9 UAT tests that were BLOCKED during Phase 08 execution to verify iPad Safari compatibility and iOS-specific behaviors.

**Precondition:** Phase 08-05 static file serving fix must be applied (config.example.yaml updated, run.ps1 fixed).

**Testing Date:** ___________

**Tester:** ___________

**Equipment Needed:**
- iPad with iOS 17+ Safari
- Windows development machine with the service running
- Same Wi-Fi network for both devices
- Optional: Wired/Bluetooth headphones for audio testing

---

## 🚀 Preparation Checklist

### Service Setup (Windows Machine)

- [ ] Start the service: `./run.ps1`
- [ ] Verify service logs show: "static interface directory verified"
- [ ] Get LAN IP address: `ipconfig` → look for "IPv4 Address" (e.g., 192.168.1.100)
- [ ] Verify http://localhost:8080/ loads the student interface (5.2KB, not test-interface.html 57KB)

### iPad Setup

- [ ] Ensure iPad is on iOS 17+ or later
- [ ] Connect iPad to same Wi-Fi network as Windows machine
- [ ] Clear Safari cache: Settings → Safari → Clear History and Website Data
- [ ] Close all Safari tabs and restart Safari
- [ ] Set iPad volume to ~50% (for audio tests)
- [ ] Ensure iPad is not muted (check mute switch)

### Test URL Preparation

**Test URL Format:** `http://<YOUR_WINDOWS_IP>:8080/`

**Example:** If your Windows IP is 192.168.1.100, use `http://192.168.1.100:8080/`

---

## 📱 Test Execution Plan

### Configuration-Unblocked Tests (6 tests)

These tests were blocked by the static file serving 404 error that was fixed in 08-05.

#### Test 3: iPad Safari Responsive Design
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Open Safari on iPad
2. Navigate to: `http://<YOUR_WINDOWS_IP>:8080/`
3. Observe page load and rendering

**Expected Results:**
- Page loads without 404 errors
- CSS styling is applied (not raw HTML)
- Clean student interface with proper layout
- "I want to dictate 30 words" button is visible and prominent

**Actual Results:**
```
[Document your observations here]
```

**Evidence:** Screenshots (attach if possible)

---

#### Test 5: Portrait Orientation Layout
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Hold iPad in portrait orientation (taller than wide)
2. Observe the layout and element positioning

**Expected Results:**
- Layout adapts to 768-1024px width
- Elements are stacked vertically
- Buttons have min-height 72px+ (comfortably tappable)
- Progress bar shows at appropriate size
- Text is readable at 22px status text size

**Actual Results:**
```
[Document your observations here]
```

**Evidence:** Screenshot of portrait layout

---

#### Test 6: Landscape Orientation Layout  
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Rotate iPad to landscape orientation (wider than tall)
2. Observe the layout adaptation

**Expected Results:**
- Layout adapts to wider aspect ratio
- `.container` switches to flex-start alignment
- Progress bar height shrinks to 18px
- Buttons remain tappable (72px+ maintained)
- No horizontal scrolling required

**Actual Results:**
```
[Document your observations here]
```

**Evidence:** Screenshot of landscape layout

---

#### Test 7: Touch Target Sizes
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Visually inspect button sizes
2. Tap each button type to test comfort
3. Try the "I want to dictate 30 words" button
4. Try any other visible buttons

**Expected Results:**
- `.kid-button` has min-height: 60px (72px on iPad)
- `.session-button` has min-height: 88px on iPad
- All buttons are comfortably tappable by a child's fingers
- No buttons are too small or require precise tapping
- Touch targets meet iOS HIG 44x44px minimum

**Actual Results:**
```
[Document your observations here]
```

**Measurements:** (optional)
- Session button height: _____ px
- Other buttons height: _____ px

---

#### Test 11: No Zoom-on-Focus
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Tap various interactive elements (buttons, inputs)
2. Observe if automatic zooming occurs
3. Try tapping buttons rapidly in succession

**Expected Results:**
- No automatic zoom occurs when focusing elements
- `touch-action: manipulation` CSS prevents iOS zoom
- Page remains stable during interaction
- No layout shifts due to zoom

**Actual Results:**
```
[Document your observations here]
```

**Tested Elements:**
- [ ] "I want to dictate 30 words" button
- [ ] Any input fields (if present)
- [ ] Any other interactive elements

---

#### Test 12: Connection Indicator States
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Navigate to the student interface
2. Click "I want to dictate 30 words" button
3. Observe connection indicator behavior

**Expected Results:**
- Connection indicator shows initial state (connecting/disconnected)
- After clicking button, indicator transitions to "connected" state
- Visual state changes with appropriate colors/labels
- No error indicators appear

**Actual Results:**
```
[Document your observations here]
```

**States Observed:**
- Initial state: _____
- After button click: _____
- Colors observed: _____

---

### Physical-Device Tests (3 tests)

These tests require the physical device to verify iOS-specific behaviors.

#### Test 8: iOS 17+ Single-Tap Unlock
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Navigate to student interface on iPad Safari
2. Tap "I want to dictate 30 words" button ONCE
3. Observe if audio starts playing

**Expected Results:**
- Audio starts playing WITHOUT requiring a second tap
- First tap unlocks AudioContext (construct + resume + silent buffer)
- Audio plays immediately on iOS 17+
- No "tap to enable audio" overlay appears

**Actual Results:**
```
[Document your observations here]
```

**Alternative Test (If Backend Audio Not Available):**
- Verify the code exists in `audio-controller.js:81-103` 
- Confirm 3-condition unlock is implemented:
  - AudioContext construct at 16kHz
  - resume() call
  - Silent buffer.start(0)
- Document: "Code review confirms iOS 17+ compatibility implementation"

**If Deferred:** Requires backend audio streaming (08-06) to be fully functional

---

#### Test 9: App-Switch Recovery
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Start a session with audio (if available)
2. During audio playback, switch to another app (home button + tap another app)
3. Wait 3 seconds
4. Return to Safari
5. Observe audio behavior

**Expected Results:**
- Either: Audio resumes automatically upon return
- Or: Tap-to-resume overlay (#audioTapToResumeOverlay) appears
- `handleVisibilityChange` listener works correctly
- `_handleStateChange` shows/hides overlay appropriately

**Actual Results:**
```
[Document your observations here]
```

**If Deferred:** Requires backend audio streaming (08-06) and active audio playback

---

#### Test 10: Audio Plays Gaplessly
**Status:** ☐ PASS ☐ FAIL ☐ DEFERRED

**Steps:**
1. Start a session with multiple words (if available)
2. Listen carefully to transitions between consecutive words
3. Observe for gaps, clicks, or pauses between words

**Expected Results:**
- No perceptible gaps between words
- Smooth transitions from one word to the next
- Gapless scheduling with precise timing (20-30 word sequence)
- AudioController plays each word seamlessly

**Actual Results:**
```
[Document your observations here]
```

**Test Details:**
- Number of words in sequence: _____
- Any gaps detected: YES/NO
- Quality of transitions: _____

**If Deferred:** Requires backend multi-word streaming (now implemented in 08-06) and TTS availability

---

## 📊 Test Results Summary

### Overall Status

**Tests Executed:** _____ / 9

**Tests Passed:** _____ 

**Tests Failed:** _____

**Tests Deferred:** _____

### Configuration-Unblocked Tests (Tests 3, 5, 6, 7, 11, 12)

| Test | Status | Notes |
|------|--------|-------|
| Test 3: Responsive Design | ☐ PASS ☐ FAIL | |
| Test 5: Portrait Layout | ☐ PASS ☐ FAIL | |
| Test 6: Landscape Layout | ☐ PASS ☐ FAIL | |
| Test 7: Touch Targets | ☐ PASS ☐ FAIL | |
| Test 11: No Zoom-on-Focus | ☐ PASS ☐ FAIL | |
| Test 12: Connection States | ☐ PASS ☐ FAIL | |

### Physical-Device Tests (Tests 8, 9, 10)

| Test | Status | Dependencies |
|------|--------|--------------|
| Test 8: iOS Unlock | ☐ PASS ☐ FAIL ☐ DEFERRED | Requires audio backend |
| Test 9: App-Switch Recovery | ☐ PASS ☐ FAIL ☐ DEFERRED | Requires audio backend |
| Test 10: Gapless Audio | ☐ PASS ☐ FAIL ☐ DEFERRED | Requires multi-word streaming |

### Issues Found

**Major Issues:**
```
[List any rendering problems, functional failures, or significant deviations from expected behavior]
```

**Minor Issues:**
```
[List any cosmetic issues, minor UX problems, or suggestions for improvement]
```

**iOS-Specific Behaviors:**
```
[Document any iOS 17+ specific behaviors or quirks observed]
```

---

## 📸 Evidence Collection

### Screenshots Required

- [ ] Portrait orientation layout
- [ ] Landscape orientation layout  
- [ ] Connection indicator states (before/after)
- [ ] Touch target demonstration (optional)

### Screen Recording (Optional)

- [ ] Record test execution for future reference
- [ ] Document audio playback behavior

---

## 🔄 Next Steps

### If All Tests Pass:
- Update 08-UAT.md with PASS results
- Mark Phase 08 verification as complete
- Proceed to next phase or feature

### If Tests Fail:
- Document specific failures with evidence
- Create gap closure plans for issues found
- Determine if fixes needed or if acceptable deviations

### If Tests Deferred:
- Document dependencies (e.g., requires backend audio)
- Plan when deferred tests can be executed
- Track in appropriate phase or sprint

---

## 📝 Additional Notes

```
[Any additional observations, context, or information that would be helpful for the development team]
```

**Testing Completed:** ___________

**Results Submitted To:** ___________

---

*This testing guide was prepared for Phase 08-07 iPad UAT Test Execution to verify the 9 previously blocked UAT tests after the 08-05 static file serving fix was applied.*