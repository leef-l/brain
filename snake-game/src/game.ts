// Snake Game Engine
export type Direction = "UP" | "DOWN" | "LEFT" | "RIGHT";
export type Position = { x: number; y: number };
export type GameState = "IDLE" | "PLAYING" | "PAUSED" | "GAME_OVER";

export interface GameConfig {
  gridWidth: number;
  gridHeight: number;
  initialSpeed: number;
  speedIncrement: number;
}

const DEFAULT_CONFIG: GameConfig = {
  gridWidth: 20,
  gridHeight: 20,
  initialSpeed: 150,
  speedIncrement: 2,
};

export class SnakeGame {
  config: GameConfig;
  snake: Position[];
  food: Position;
  direction: Direction;
  nextDirection: Direction;
  state: GameState;
  score: number;
  highScore: number;
  private moveTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(config: Partial<GameConfig> = {}) {
    this.config = { ...DEFAULT_CONFIG, ...config };
    this.snake = [];
    this.food = { x: 0, y: 0 };
    this.direction = "RIGHT";
    this.nextDirection = "RIGHT";
    this.state = "IDLE";
    this.score = 0;
    this.highScore = this.loadHighScore();
    this.initSnake();
    this.spawnFood();
  }

  private loadHighScore(): number {
    try {
      const stored = localStorage.getItem("snake_high_score");
      return stored ? parseInt(stored, 10) : 0;
    } catch {
      return 0;
    }
  }

  private saveHighScore(): void {
    try {
      localStorage.setItem("snake_high_score", String(this.highScore));
    } catch {
      // localStorage may not be available
    }
  }

  private initSnake(): void {
    const startX = Math.floor(this.config.gridWidth / 2);
    const startY = Math.floor(this.config.gridHeight / 2);
    this.snake = [
      { x: startX, y: startY },
      { x: startX - 1, y: startY },
      { x: startX - 2, y: startY },
    ];
  }

  // Returns true if food was spawned, false if board is full
  private spawnFood(): boolean {
    const occupied = new Set(
      this.snake.map((p) => `${p.x},${p.y}`)
    );
    const available: Position[] = [];
    for (let x = 0; x < this.config.gridWidth; x++) {
      for (let y = 0; y < this.config.gridHeight; y++) {
        if (!occupied.has(`${x},${y}`)) {
          available.push({ x, y });
        }
      }
    }
    if (available.length === 0) {
      return false;
    }
    this.food = available[Math.floor(Math.random() * available.length)];
    return true;
  }

  setDirection(dir: Direction): void {
    const opposites: Record<Direction, Direction> = {
      UP: "DOWN",
      DOWN: "UP",
      LEFT: "RIGHT",
      RIGHT: "LEFT",
    };
    // Prevent 180-degree turns
    if (opposites[dir] !== this.direction) {
      this.nextDirection = dir;
    }
  }

  start(): void {
    if (this.state === "GAME_OVER") {
      this.reset();
    }
    this.state = "PLAYING";
    this.scheduleMove();
  }

  pause(): void {
    if (this.state === "PLAYING") {
      this.state = "PAUSED";
      if (this.moveTimer) {
        clearTimeout(this.moveTimer);
        this.moveTimer = null;
      }
    }
  }

  resume(): void {
    if (this.state === "PAUSED") {
      this.state = "PLAYING";
      this.scheduleMove();
    }
  }

  reset(): void {
    if (this.moveTimer) {
      clearTimeout(this.moveTimer);
      this.moveTimer = null;
    }
    this.initSnake();
    this.direction = "RIGHT";
    this.nextDirection = "RIGHT";
    this.score = 0;
    this.state = "IDLE";
    this.spawnFood();
  }

  private scheduleMove(): void {
    const speed = Math.max(
      50,
      this.config.initialSpeed - this.score * this.config.speedIncrement
    );
    this.moveTimer = setTimeout(() => {
      this.tick();
      if (this.state === "PLAYING") {
        this.scheduleMove();
      }
    }, speed);
  }

  tick(): boolean {
    if (this.state !== "PLAYING") return false;

    this.direction = this.nextDirection;

    const head = this.snake[0];
    const newHead: Position = { ...head };

    switch (this.direction) {
      case "UP":
        newHead.y--;
        break;
      case "DOWN":
        newHead.y++;
        break;
      case "LEFT":
        newHead.x--;
        break;
      case "RIGHT":
        newHead.x++;
        break;
    }

    // Wall collision
    if (
      newHead.x < 0 ||
      newHead.x >= this.config.gridWidth ||
      newHead.y < 0 ||
      newHead.y >= this.config.gridHeight
    ) {
      this.gameOver();
      return false;
    }

    // Self collision (skip tail since it will move)
    const willGrow = newHead.x === this.food.x && newHead.y === this.food.y;
    const checkBody = willGrow ? this.snake : this.snake.slice(0, -1);
    for (const segment of checkBody) {
      if (segment.x === newHead.x && segment.y === newHead.y) {
        this.gameOver();
        return false;
      }
    }

    // Move
    this.snake.unshift(newHead);

    if (willGrow) {
      this.score += 10;
      if (this.score > this.highScore) {
        this.highScore = this.score;
        this.saveHighScore();
      }
      if (!this.spawnFood()) {
        // Board is full - player wins
        this.gameOver();
        return false;
      }
    } else {
      this.snake.pop();
    }

    return true;
  }

  private gameOver(): void {
    this.state = "GAME_OVER";
    if (this.moveTimer) {
      clearTimeout(this.moveTimer);
      this.moveTimer = null;
    }
    if (this.score > this.highScore) {
      this.highScore = this.score;
      this.saveHighScore();
    }
  }

  getGrid(): number[][] {
    const grid = Array.from({ length: this.config.gridHeight }, () =>
      Array(this.config.gridWidth).fill(0)
    );
    // Food
    grid[this.food.y][this.food.x] = 2;
    // Snake body
    for (let i = 0; i < this.snake.length; i++) {
      const s = this.snake[i];
      if (s.y >= 0 && s.y < this.config.gridHeight && s.x >= 0 && s.x < this.config.gridWidth) {
        grid[s.y][s.x] = i === 0 ? 3 : 1; // 3=head, 1=body
      }
    }
    return grid;
  }

  destroy(): void {
    if (this.moveTimer) {
      clearTimeout(this.moveTimer);
      this.moveTimer = null;
    }
  }
}
