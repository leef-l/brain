import { useCallback, useEffect, useRef } from "react";
import { Direction } from "../game";
import "./TouchControls.css";

interface TouchControlsProps {
  onDirection: (dir: Direction) => void;
  onAction: () => void; // start / pause toggle
}

export default function TouchControls({
  onDirection,
  onAction,
}: TouchControlsProps) {
  const startX = useRef<number | null>(null);
  const startY = useRef<number | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  // Swipe detection on the grid area
  const handleTouchStart = useCallback((e: TouchEvent) => {
    const touch = e.touches[0];
    startX.current = touch.clientX;
    startY.current = touch.clientY;
  }, []);

  const handleTouchEnd = useCallback(
    (e: TouchEvent) => {
      if (startX.current === null || startY.current === null) return;

      const touch = e.changedTouches[0];
      const dx = touch.clientX - startX.current;
      const dy = touch.clientY - startY.current;
      const minSwipe = 30;

      if (Math.abs(dx) < minSwipe && Math.abs(dy) < minSwipe) {
        // It's a tap, not a swipe - treat as action
        onAction();
      } else if (Math.abs(dx) > Math.abs(dy)) {
        onDirection(dx > 0 ? "RIGHT" : "LEFT");
      } else {
        onDirection(dy > 0 ? "DOWN" : "UP");
      }

      startX.current = null;
      startY.current = null;
    },
    [onDirection, onAction]
  );

  // Attach swipe listeners to the document body
  useEffect(() => {
    document.addEventListener("touchstart", handleTouchStart, { passive: true });
    document.addEventListener("touchend", handleTouchEnd, { passive: true });
    return () => {
      document.removeEventListener("touchstart", handleTouchStart);
      document.removeEventListener("touchend", handleTouchEnd);
    };
  }, [handleTouchStart, handleTouchEnd]);

  const btnClass = (dir: Direction) => `touch-btn touch-btn--${dir.toLowerCase()}`;

  return (
    <div className="touch-controls" ref={containerRef}>
      <div className="touch-dpad">
        <button
          className={btnClass("UP")}
          onClick={() => onDirection("UP")}
          aria-label="Move up"
        >
          ▲
        </button>
        <div className="touch-dpad-row">
          <button
            className={btnClass("LEFT")}
            onClick={() => onDirection("LEFT")}
            aria-label="Move left"
          >
            ◀
          </button>
          <button
            className="touch-btn touch-btn--action"
            onClick={onAction}
            aria-label="Start / Pause"
          >
            ▶
          </button>
          <button
            className={btnClass("RIGHT")}
            onClick={() => onDirection("RIGHT")}
            aria-label="Move right"
          >
            ▶
          </button>
        </div>
        <button
          className={btnClass("DOWN")}
          onClick={() => onDirection("DOWN")}
          aria-label="Move down"
        >
          ▼
        </button>
      </div>
    </div>
  );
}
