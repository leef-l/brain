import { useState, useCallback, useRef } from 'react';
import StartScreen from './components/StartScreen';
import ScoreDisplay from './components/ScoreDisplay';
import Timer from './components/Timer';
import DifficultySelector from './components/DifficultySelector';
import GameBoard from './components/GameBoard';
import GameOverOverlay from './components/GameOverOverlay';
import './App.css';

const DIFFICULTY_PAIRS = { easy: 4, medium: 6, hard: 8 };

export default function App() {
  // Game phase: 'start' | 'playing' | 'finished'
  const [phase, setPhase] = useState('start');
  const [difficulty, setDifficulty] = useState('medium');
  const pairCount = DIFFICULTY_PAIRS[difficulty];

  const [score, setScore] = useState(0);
  const [bestScore, setBestScore] = useState(() => {
    const saved = localStorage.getItem('memory-match-best');
    return saved ? Number(saved) : 0;
  });
  const [moves, setMoves] = useState(0);
  const [matchedPairs, setMatchedPairs] = useState(0);
  const [gameOver, setGameOver] = useState(false);
  const [isNewBest, setIsNewBest] = useState(false);
  const [elapsedTime, setElapsedTime] = useState(0);
  const [resetSignal, setResetSignal] = useState(0);

  // ---- Callbacks ----
  const onScoreChange = useCallback((delta) => {
    setScore((prev) => Math.max(0, prev + delta));
  }, []);

  const onMove = useCallback(() => {
    setMoves((prev) => prev + 1);
  }, []);

  const onPairMatched = useCallback(() => {
    setMatchedPairs((prev) => {
      const next = prev + 1;
      if (next === pairCount) {
        // Handled via onGameComplete
      }
      return next;
    });
  }, [pairCount]);

  const onGameComplete = useCallback(() => {
    setGameOver(true);
    setPhase('finished');
    // Check/save best score with a small delay so state is settled
    setTimeout(() => {
      setScore((currentScore) => {
        const prevBest = Number(localStorage.getItem('memory-match-best') || 0);
        if (currentScore > prevBest) {
          localStorage.setItem('memory-match-best', String(currentScore));
          setBestScore(currentScore);
          setIsNewBest(true);
        }
        return currentScore;
      });
    }, 50);
  }, []);

  const onTimeUpdate = useCallback((seconds) => {
    setElapsedTime(seconds);
  }, []);

  // ---- Game control ----
  const handleStart = useCallback((diff) => {
    setDifficulty(diff);
    setScore(0);
    setMoves(0);
    setMatchedPairs(0);
    setGameOver(false);
    setIsNewBest(false);
    setElapsedTime(0);
    setPhase('playing');
    setResetSignal((prev) => prev + 1);
  }, []);

  const handleRestart = useCallback(() => {
    setScore(0);
    setMoves(0);
    setMatchedPairs(0);
    setGameOver(false);
    setIsNewBest(false);
    setElapsedTime(0);
    setPhase('playing');
    setResetSignal((prev) => prev + 1);
  }, []);

  const handleChangeDifficulty = useCallback((diff) => {
    // Only allow changing before game starts
    if (phase === 'start') {
      setDifficulty(diff);
    }
  }, [phase]);

  const handleBackToStart = useCallback(() => {
    setPhase('start');
    setGameOver(false);
  }, []);

  // ---- Render ----
  return (
    <div className="app">
      {/* Header — always visible */}
      <h1 className="app__title">
        🧠 <span>Memory</span> Match
      </h1>

      {phase === 'start' && (
        <StartScreen
          onStart={handleStart}
          difficulty={difficulty}
          onChangeDifficulty={handleChangeDifficulty}
        />
      )}

      {phase !== 'start' && (
        <>
          <div className="app__controls">
            <Timer
              running={!gameOver}
              resetSignal={resetSignal}
              onTimeUpdate={onTimeUpdate}
            />
            <DifficultySelector
              difficulty={difficulty}
              onChange={handleChangeDifficulty}
              disabled
            />
          </div>

          <ScoreDisplay
            score={score}
            bestScore={bestScore}
            moves={moves}
            matchedPairs={matchedPairs}
            totalPairs={pairCount}
          />

          <GameBoard
            pairCount={pairCount}
            onScoreChange={onScoreChange}
            onMove={onMove}
            onPairMatched={onPairMatched}
            onGameComplete={onGameComplete}
            resetSignal={resetSignal}
          />

          <div className="app__actions">
            <button className="app__restart" onClick={handleRestart}>
              Restart Game
            </button>
            <button className="app__back" onClick={handleBackToStart}>
              Main Menu
            </button>
          </div>
        </>
      )}

      <GameOverOverlay
        visible={gameOver}
        score={score}
        moves={moves}
        elapsedTime={elapsedTime}
        totalPairs={pairCount}
        isNewBest={isNewBest}
        onRestart={handleRestart}
      />
    </div>
  );
}
